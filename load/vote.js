// Load test for the vote hot path, shaped to the arrival-time curve
// from docs/ai/02-load-model.md §3 — votes are NOT flat across the
// voting window: a fast rise in the first 15s, a plateau while the spot
// is still airing (30-60s), then a long tail after it ends.
//
// ramping-arrival-rate (not constant-vus/constant-arrival-rate) because
// what we're modeling is "how many voters arrive per second", which is
// what actually stresses the server — a fixed VU count just measures
// how fast k6's own VUs can loop.
//
// vegeta/wrk don't fit here (docs/ai/01-stack.md): voting is a two-step
// flow (fetch a token, then spend it), not a flat single-URL hammer.
//
// IMPORTANT — rate limiting and this test: docs/ai/03-deduplication.md's
// per-(poll_id, ip) limiter treats "one machine firing lots of requests"
// as exactly the pattern it exists to stop, and every k6 VU on this
// laptop shares one source IP. That's not a bug to work around
// silently: it's the limiter doing its job. To measure the *hot path's*
// ceiling rather than the *limiter's* threshold, run the stack with
// RATE_LIMIT_RPS/RATE_LIMIT_BURST raised for the duration of the load
// test only (see load/README.md).
//
// Usage:
//   BASE_URL=http://localhost:8080 ADMIN_TOKEN=... k6 run load/vote.js
//   TARGET_VOTES=20000 k6 run load/vote.js   # scale the whole curve up/down
import http from 'k6/http';
import { check, sleep } from 'k6';
import { Counter, Trend } from 'k6/metrics';

const BASE_URL = __ENV.BASE_URL || 'http://localhost:8080';
const ADMIN_TOKEN = __ENV.ADMIN_TOKEN || 'local-dev-admin-token';

// Total votes across the whole run — every stage's rate is derived from
// this proportionally to the §3 curve. Small by default so the script
// is safe to run without configuration; raise it to find the
// single-instance ceiling (docs/ai/02-load-model.md §7).
const TARGET_VOTES = parseInt(__ENV.TARGET_VOTES || '3000', 10);

const votesAccepted = new Counter('votes_accepted');
const votesRejected = new Counter('votes_rejected');
const voteErrors = new Counter('vote_errors');
const voteDuration = new Trend('vote_duration', true);

// docs/ai/02-load-model.md §3's table, as cumulative seconds since the
// QR code appears rather than per-segment shares — easier to turn into
// stage durations below.
const CURVE = [
  { untilSec: 15, share: 0.10 }, // fastest risers, phone already in hand
  { untilSec: 30, share: 0.25 }, // main wave starts
  { untilSec: 60, share: 0.30 }, // peak — spot still airing
  { untilSec: 120, share: 0.20 }, // spot over, "finished scanning"
  { untilSec: 300, share: 0.15 }, // long tail, delayed voting
];

function buildStages(totalVotes) {
  const stages = [];
  let prevUntil = 0;
  for (const seg of CURVE) {
    const duration = seg.untilSec - prevUntil;
    const votesInSeg = totalVotes * seg.share;
    const ratePerSec = Math.max(1, Math.round(votesInSeg / duration));
    stages.push({ target: ratePerSec, duration: `${duration}s` });
    prevUntil = seg.untilSec;
  }
  return stages;
}

export const options = {
  scenarios: {
    vote_arrival: {
      executor: 'ramping-arrival-rate',
      startRate: 1,
      timeUnit: '1s',
      preAllocatedVUs: 100,
      maxVUs: 2000,
      stages: buildStages(TARGET_VOTES),
    },
  },
  thresholds: {
    // The SLO from docs/ai/02-load-model.md §7. k6 reports pass/fail;
    // the actual p99 value is what goes in the README regardless of
    // whether it passed — a measurement, not a gate to satisfy.
    vote_duration: ['p(99)<200'],
  },
};

export function setup() {
  const headers = { 'Content-Type': 'application/json', Authorization: `Bearer ${ADMIN_TOKEN}` };

  const createRes = http.post(
    `${BASE_URL}/v1/admin/polls`,
    JSON.stringify({
      question: 'k6 load test',
      options: [{ label: 'A' }, { label: 'B' }, { label: 'C' }],
      voting_window_seconds: 600,
    }),
    { headers },
  );
  if (createRes.status !== 201) {
    throw new Error(`setup: create poll failed: ${createRes.status} ${createRes.body}`);
  }
  const pollId = createRes.json('id');
  const optionCount = createRes.json('options').length;

  const scheduledAt = new Date(Date.now() + 2000).toISOString();
  const scheduleRes = http.post(
    `${BASE_URL}/v1/admin/polls/${pollId}/transitions`,
    JSON.stringify({ to: 'scheduled', scheduled_at: scheduledAt }),
    { headers },
  );
  if (scheduleRes.status !== 200) {
    throw new Error(`setup: schedule poll failed: ${scheduleRes.status} ${scheduleRes.body}`);
  }

  // Deliberately no manual "to: open" call here: the auto-opener
  // (review finding B4) transitions the poll itself once scheduled_at
  // arrives, exercising the real production path instead of a
  // test-only shortcut. Poll GET until it's actually open rather than a
  // fixed sleep, since the auto-opener's own tick interval adds jitter
  // on top of the 2s schedule offset.
  let state = '';
  for (let i = 0; i < 20 && state !== 'open'; i++) {
    sleep(0.5);
    const getRes = http.get(`${BASE_URL}/v1/admin/polls/${pollId}`, { headers });
    state = getRes.json('state');
  }
  if (state !== 'open') {
    throw new Error(`setup: poll did not auto-open in time (last state: ${state})`);
  }

  console.log(`poll ${pollId} open, ${optionCount} options, target ${TARGET_VOTES} votes`);
  return { pollId, optionCount };
}

export default function (data) {
  // Every iteration is a distinct simulated voter: a fresh token, then
  // one spend of it — mirrors the real two-step flow
  // (docs/ai/01-stack.md), not a token reused across iterations.
  const tokenRes = http.post(`${BASE_URL}/v1/polls/${data.pollId}/token`);
  if (tokenRes.status !== 201) {
    voteErrors.add(1);
    return;
  }
  const token = tokenRes.json('token');
  const optionId = 1 + Math.floor(Math.random() * data.optionCount);

  const voteRes = http.post(
    `${BASE_URL}/v1/polls/${data.pollId}/votes`,
    JSON.stringify({ option_id: optionId }),
    { headers: { 'Content-Type': 'application/json', 'X-Vote-Token': token } },
  );
  voteDuration.add(voteRes.timings.duration);

  if (voteRes.status === 201) {
    votesAccepted.add(1);
  } else if (voteRes.status === 409 || voteRes.status === 429) {
    votesRejected.add(1);
  } else {
    voteErrors.add(1);
  }

  check(voteRes, { 'vote accepted': (r) => r.status === 201 });
}

export function teardown(data) {
  http.post(
    `${BASE_URL}/v1/admin/polls/${data.pollId}/transitions`,
    JSON.stringify({ to: 'closed' }),
    { headers: { 'Content-Type': 'application/json', Authorization: `Bearer ${ADMIN_TOKEN}` } },
  );
}

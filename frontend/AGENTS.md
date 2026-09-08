# Frontend guidance

## Principles

- Keep request flows explicit and searchable.
- Treat `../api/openapi.yaml` as the only wire-contract source of truth.
- Regenerate `src/lib/api/schema.generated.ts` after contract changes.
- Keep the public voting bundle free of admin-only dependencies.
- Prefer existing shadcn/Base UI components before adding abstractions.
- Do not embed `ADMIN_TOKEN` or vote tokens into logs, URLs, or build output.

## Map

- `src/public/`: public poll route, token lifecycle, and voting UI.
- `src/admin/`: admin auth, routes, poll management, and results.
- `src/components/ui/`: curated shadcn/Base UI source.
- `src/lib/api/`: generated contract types and the HTTP client.
- `styles/globals.css`: Tailwind import and neutral PBS theme.
- `build.ts`: fixed two-entrypoint production build.
- `tests/`: Bun unit and API boundary tests.
- `e2e/`: Playwright desktop/mobile flows.

## Verification

Run after frontend changes:

```bash
bun run lint
bun run typecheck
bun run test
bun run test:e2e
bun run build
```

Do not hand-edit `bun.lock`, `dist/`, or `src/lib/api/schema.generated.ts`.

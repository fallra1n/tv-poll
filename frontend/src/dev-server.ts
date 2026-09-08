import adminPage from "./admin/index.html";
import publicPage from "./index.html";

const port = Number(process.env.PORT ?? 3000);

if (!Number.isInteger(port) || port < 1 || port > 65_535) {
  throw new Error("PORT must be an integer between 1 and 65535");
}

const server = Bun.serve({
  port,
  routes: {
    "/admin": adminPage,
    "/admin/*": adminPage,
    "/polls/*": publicPage,
    "/*": publicPage,
  },
  development: {
    hmr: true,
    console: true,
  },
});

console.log(`Frontend running at ${server.url}`);

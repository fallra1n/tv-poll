import { GlobalRegistrator } from "@happy-dom/global-registrator";

process.env.BUN_PUBLIC_API_URL ??= "http://localhost:8080";
GlobalRegistrator.register();

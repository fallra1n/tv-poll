import { Component, type ReactNode } from "react";

type Props = {
  title: string;
  message: string;
  children: ReactNode;
};

type State = {
  hasError: boolean;
};

// Catches render-time throws only — it cannot catch the module-scope
// `throw` in src/lib/config.ts (a misconfigured API URL), which happens
// before React ever mounts. That gap is accepted; this covers the other,
// more likely white-screen path (a render crash from bad server data).
export class ErrorBoundary extends Component<Props, State> {
  override state: State = { hasError: false };

  static getDerivedStateFromError() {
    return { hasError: true };
  }

  override componentDidCatch(error: unknown) {
    console.error("Unhandled render error", error);
  }

  override render() {
    if (!this.state.hasError) {
      return this.props.children;
    }

    return (
      <main className="grid min-h-dvh place-items-center px-4 py-8 sm:px-6">
        <div className="w-full max-w-lg rounded-xl bg-card p-8 text-center text-sm text-card-foreground shadow-xs ring-1 ring-foreground/10">
          <h1 className="text-2xl font-semibold">{this.props.title}</h1>
          <p className="mt-3 text-muted-foreground">{this.props.message}</p>
        </div>
      </main>
    );
  }
}

import { Component, type ReactNode } from "react";
import { RotateCcw, TriangleAlert } from "lucide-react";
import { Button, EmptyState } from "./ui";

interface Props {
  children: ReactNode;
  /** Changing the key resets the boundary (used per-route). */
  resetKey?: string;
}

interface State {
  error: Error | null;
}

/**
 * Route-level error boundary: catches render crashes in a page, shows a
 * branded fallback and offers a retry that remounts the failed subtree.
 */
export class ErrorBoundary extends Component<Props, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: Error): State {
    return { error };
  }

  componentDidCatch(error: Error, info: { componentStack: string }) {
    console.error("[ui] page crashed", error, info.componentStack);
  }

  componentDidUpdate(prev: Props) {
    if (prev.resetKey !== this.props.resetKey && this.state.error) {
      this.setState({ error: null });
    }
  }

  render() {
    if (this.state.error) {
      return (
        <div className="mx-auto max-w-lg pt-16">
          <EmptyState
            icon={<TriangleAlert className="h-6 w-6" />}
            title="This page crashed"
            body={this.state.error.message}
            action={
              <Button
                variant="secondary"
                className="mt-2"
                onClick={() => this.setState({ error: null })}
              >
                <RotateCcw className="h-4 w-4" aria-hidden /> Retry
              </Button>
            }
          />
        </div>
      );
    }
    return this.props.children;
  }
}

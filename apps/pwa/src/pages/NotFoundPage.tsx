import { Link } from "react-router-dom";
import { EmptyState } from "../components/ui";

export default function NotFoundPage() {
  return (
    <div className="mx-auto max-w-lg pt-16">
      <EmptyState
        title="Page not found"
        body="This route does not exist — or its module is disabled. Use the navigation to reach an enabled module."
        action={
          <Link
            to="/"
            className="mt-2 rounded-lg border border-stone-300 px-3.5 py-2 text-sm font-medium text-stone-700 hover:bg-surface-sunken"
          >
            Back to home
          </Link>
        }
      />
    </div>
  );
}

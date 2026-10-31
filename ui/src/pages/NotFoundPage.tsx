import { Link, useLocation } from "react-router";

import { Page } from "../components/Page.tsx";

/** NotFoundPage answers every path no route matches. */
export function NotFoundPage() {
  const { pathname } = useLocation();
  return (
    <Page title="Page not found">
      <p>
        There is no page at <code>{pathname}</code>. <Link to="/">Back to the overview</Link>.
      </p>
    </Page>
  );
}

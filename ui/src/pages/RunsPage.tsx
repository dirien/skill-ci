import { Page, Todo } from "../components/Page.tsx";

/** RunsPage is /runs: the runs table with filters and pagination, and a trial logs column. */
export function RunsPage() {
  return (
    <Page title="Runs">
      <Todo what="the runs page" />
    </Page>
  );
}

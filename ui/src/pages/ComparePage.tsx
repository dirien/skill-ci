import { useParams } from "react-router";

import { Page, Todo } from "../components/Page.tsx";

/** ComparePage is /runs/{name}/cases/{case}/compare[?attempt=&with=&without=]: both arms side by side. */
export function ComparePage() {
  const { case: caseId = "" } = useParams();
  return (
    <Page title={`Compare ${caseId}`}>
      <Todo what="with and without skill side by side" />
    </Page>
  );
}

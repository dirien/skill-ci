import { useParams } from "react-router";

import { Page, Todo } from "../components/Page.tsx";

/** RunPage is /runs/{name}[?job=]: one SkillEval with its trials grid. */
export function RunPage() {
  const { name = "" } = useParams();
  return (
    <Page title={name}>
      <Todo what="scores, tiers, network, conditions, dimensions, attempts and the trials grid" />
    </Page>
  );
}

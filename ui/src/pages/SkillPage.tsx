import { useParams } from "react-router";

import { Page, Todo } from "../components/Page.tsx";

/** SkillPage is /skills/{skill}: versions x entries matrix, channel tags and policy decisions. */
export function SkillPage() {
  const { skill = "" } = useParams();
  return (
    <Page title={skill}>
      <Todo what="the versions by entries matrix and the policy decisions" />
    </Page>
  );
}

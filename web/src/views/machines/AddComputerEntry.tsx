// "Add computer" from a page that is not "Machines" (docs/assistant/onboarding.md 4.5 "Rewriting empty states"): the Agents page and "New agent" with no
// computer open the machines page's own flow (AddComputer) where the hub has it (feature machines_add), else the plain
// how-to, like the machines page's empty state; and the empty state those pages show.
import { useState } from "react";
import { t } from "../../i18n";
import { canAddComputer } from "../../lib/add-computer";
import { readinessChanged } from "../../lib/readiness";
import { useStore } from "../../lib/store";
import { Button } from "../../ui/atoms";
import { Empty } from "../assistant/parts";
import { HelpDialog } from "../assistant/Readiness";
import { lazyDialog } from "../LazyOverlay";

// the dialog loads on its first open: the sidebar and New agent draw without it
const AddComputer = lazyDialog(() => import("./AddComputer").then((m) => m.AddComputer));

/** `open` starts adding a computer; `dialog` is rendered once beside the button (outside any other dialog). */
export function useAddComputer(): { open: () => void; dialog: React.ReactNode } {
  const canAdd = useStore((s) => canAddComputer(s.features));
  const [adding, setAdding] = useState(false);
  const [helping, setHelping] = useState(false);
  return {
    open: () => (canAdd ? setAdding(true) : setHelping(true)),
    dialog: (
      <>
        <AddComputer
          open={adding}
          onClose={() => {
            setAdding(false);
            void readinessChanged(); // the main conversation's card follows a computer added here
          }}
        />
        {helping && <HelpDialog topic="add_computer" machine="" step={null} onClose={() => setHelping(false)} />}
      </>
    ),
  };
}

/** "No computers yet" + "Add computer", the machines page's empty state, for a page that needs a computer to show anything. */
export function NoComputers({ body, onAdd }: { body: string; onAdd: () => void }) {
  return (
    <div data-testid="no-computers">
      <Empty
        icon="machine"
        title={t("machines.empty_title")}
        action={
          <Button tone="primary" icon="plus" onClick={onAdd}>
            {t("machines.add")}
          </Button>
        }
      >
        {body}
      </Empty>
    </div>
  );
}

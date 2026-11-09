import { useEffect, useRef, useState } from "react";
import { credentialOwner } from "../lib/api";
import { refreshTodos } from "../lib/store";

/** A field acknowledges only the local revision sent by its own save. */
export function useTodoField({
  remote, owner, allowEmpty, write, onError,
}: {
  remote: string;
  owner: string;
  allowEmpty: boolean;
  write: (value: string) => Promise<string>;
  onError: (error: unknown) => void;
}) {
  const draft = useRef({ value: remote, remote, revision: 0, savedRevision: 0, saving: false, blurredRevision: -1 });
  const [state, setState] = useState(draft.current);
  const active = useRef(true);
  const publish = () => setState({ ...draft.current });
  const current = () => active.current && owner === credentialOwner();

  useEffect(() => {
    active.current = true;
    return () => { active.current = false; };
  }, []);

  useEffect(() => {
    const field = draft.current;
    field.remote = remote;
    if (field.revision === field.savedRevision) field.value = remote;
    setState({ ...field });
  }, [remote]);

  const edit = (value: string) => {
    draft.current.value = value;
    draft.current.revision++;
    draft.current.blurredRevision = -1;
    publish();
  };

  const save = async () => {
    const field = draft.current;
    if (!current()) return;
    if (field.saving) {
      field.blurredRevision = field.revision;
      return;
    }
    const value = field.value.trim();
    if ((!allowEmpty && !value) || value === (allowEmpty ? field.remote.trim() : field.remote)) {
      field.value = field.remote;
      field.savedRevision = field.revision;
      publish();
      return;
    }
    const revision = field.revision;
    // Admission is synchronous: two blur events in one render cannot send twice.
    field.saving = true;
    publish();
    let succeeded = false;
    try {
      const saved = await write(value);
      if (!current()) return;
      field.remote = saved;
      field.savedRevision = revision;
      if (field.revision === revision) field.value = saved;
      publish();
      await refreshTodos();
      succeeded = true;
    } catch (error) {
      if (current()) onError(error);
    } finally {
      if (current()) {
        field.saving = false;
        publish();
        // A newer edit that already blurred still needs saving; typing again retires that intent.
        if (succeeded && field.blurredRevision === field.revision && field.revision !== revision) void save();
      }
    }
  };

  return { value: state.value, saving: state.saving, edit, save };
}

-- A question nobody answered became a to-do with the asking conversation as its origin (#1617), and an origin is what
-- makes a to-do a task of that conversation: the apps showed it in its task dock with a Message that always failed, and
-- an agent handed it had it closed by the done judgment with the asker never told (#1852, #1909). It is no task: the
-- asking conversation is kept in its record (the assistant.ask_todos setting, server/internal/assistant/ask_todo.go),
-- which is where its answer goes from. The ones made before lose their origin.
UPDATE todos SET origin = '' WHERE starts_with(id, 'todo_ask_') AND origin <> '';

Run commands on one of the user's computers that need a password only the user knows, typically its sudo password. Never ask for a password in the conversation, in ask_user or anywhere else: call this instead. The user gets a card on their iPhone showing the computer, your purpose and every command verbatim, types the password there and taps Run; that computer's node then runs exactly those commands in order, each as the user with /bin/sh -c in the home directory and PATH=/usr/bin:/bin:/usr/sbin:/sbin (give other tools by their absolute path). Only a command that runs sudo with -S gets the password, as a line on its stdin; every other command gets an empty stdin. Write sudo as `sudo -S -k -p '' …`, one sudo per command and never after a pipe (sudo would read the pipe; run a pipeline as root with `sudo -S -k -p '' sh -c '…'`); a sudo without -S is refused. Where a sudoers rule says NOPASSWD, sudo does not read stdin and the command it runs gets the password instead, so do not give such a command anything that reads stdin. Commands are printable ASCII only (letters, digits, punctuation, spaces), so the card shows exactly what runs. The first command that fails stops the run. Never write a command that prints, stores or sends the password. You never see the password: the result is JSON with status (ran, failed, declined, expired or refused), the computer, a message and, for ran and failed, each command's exit_code, timed_out and output (stdout and stderr, clipped to its end); when a command's output held the password, none of it comes back (output_withheld: true). A command stopped at the timeout may still be running as root: check its state before asking again. declined, expired and refused mean nothing ran: tell the user, and ask again only if they want to. Windows computers cannot run it.

```json
{
  "type": "object",
  "required": ["machine", "purpose", "commands"],
  "properties": {
    "machine": {
      "type": "string",
      "description": "the computer to run on: its id or name, as agent_start takes it"
    },
    "purpose": {
      "type": "string",
      "description": "one sentence the card shows: why the password is needed"
    },
    "commands": {
      "type": "array",
      "items": { "type": "string" },
      "minItems": 1,
      "maxItems": 5,
      "description": "1 to 5 one-line shell commands of printable ASCII, at most 1000 characters each, run in order"
    },
    "delivery": {
      "type": "string",
      "enum": ["stdin"],
      "description": "how the password reaches the commands: stdin, the default and only way"
    },
    "timeout_seconds": {
      "type": "integer",
      "minimum": 1,
      "maximum": 1800,
      "description": "for the whole run, default 300"
    }
  },
  "additionalProperties": false
}
```

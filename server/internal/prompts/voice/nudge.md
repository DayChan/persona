{{- /* The voice model owes the user an answer and has said nothing (docs/assistant/voice.md "Context for the fast
layer" rule 9, voice/nudge.go): the user finished a line or typed one (user), or something was handed to it (result: a
tool's or a turn's result, a refusal, a status), and no caption, audio or function call followed for a while. Sent once,
as a say, which the measured model always speaks after. Nothing of what was said or handed over: it has that already. */ -}}
{{- if eq .Lang "en" -}}
{{- if eq .Cause "result" -}}
You have not told the user what was just handed to you yet, and they are waiting for it. Tell them now in a sentence or two; do not call any tools, and do not mention this reminder.
{{- else -}}
The user is waiting for your answer to what they just said, and you have not said anything. Answer now in a sentence or two; if you did not catch it, ask them to say it again. Do not mention this reminder.
{{- end -}}
{{- else if eq .Lang "ja" -}}
{{- if eq .Cause "result" -}}
さっき渡した内容をまだユーザーに伝えていません。ユーザーはそれを待っています。いますぐ一、二文で伝えてください。ツールは呼ばず、このリマインダーにも触れないでください。
{{- else -}}
ユーザーはさっき言ったことへの返事を待っていますが、あなたはまだ何も言っていません。いますぐ一、二文で答えてください。聞き取れなかったなら、もう一度言ってもらってください。このリマインダーには触れないでください。
{{- end -}}
{{- else -}}
{{- if eq .Cause "result" -}}
刚才交给你的内容你还没告诉用户，用户在等。现在就用一两句话告诉用户，不要调用工具，也不要提这条提醒。
{{- else -}}
用户在等你回应他刚才说的话，你还一句都没说。现在就回答，一两句话就好；没听清就请他再说一遍。不要提这条提醒。
{{- end -}}
{{- end -}}

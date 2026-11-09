package assistant

import (
	"context"
	"fmt"
	"path"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Attachments (#2437): pictures and files the user gave the assistant, handed to an agent on whichever computer it
// runs. The assistant names them by their paths on its own computer (task_plan, agent_start and agent_send take
// attachments); a path of one written only in the prompt or text counts too, since a model does not always use the
// parameter. On the assistant's computer the paths go to the agent as they are. On another one the manager copies the
// files there first (file.chunk from the assistant's node, file.upload with dest=terminal on the agent's:
// uploads/terminal/<date>/, swept after 7 days) and the text names the copies, so a path that exists only where the
// assistant runs never reaches an agent that cannot open it. A copy that fails does not stop the hand-off: the agent
// is told which named attachment is missing.

// attachmentsMost is how many files one task or message carries.
const attachmentsMost = 8

// attachmentMostBytes is the most one may weigh: what an upload into uploads/terminal takes.
const attachmentMostBytes = proto.MaxTerminalUploadBytes

// appUploadName is the name the node gives a file the app uploads (node/upload.go): <stem>-YYYYMMDD-HHMMSS[-xxxx][.ext].
var appUploadName = lazyre.New(`-\d{8}-\d{6}(-[0-9a-f]{4})?(\.[A-Za-z0-9]+)*$`)

// textPath is an absolute path in a prompt or a message: after a space, a quote, a bracket or a colon (Chinese ones
// too), up to the next of them.
var textPath = lazyre.New(`(?:^|[\s"'“‘(（：:，,、])(~?/[^\s"'“”‘’()（）<>，。；、,;]+)`)

// cleanAttachments trims the paths, drops repeats and empties, and refuses more than attachmentsMost or one that is not
// absolute (~/ is the home of the assistant's computer).
func cleanAttachments(in []string) ([]string, error) {
	var out []string
	seen := map[string]bool{}
	for _, p := range in {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			continue
		}
		if !strings.HasPrefix(p, "/") && !strings.HasPrefix(p, "~/") {
			return nil, fmt.Errorf("attachment %q is not an absolute path on your computer", p)
		}
		seen[p] = true
		out = append(out, p)
	}
	if len(out) > attachmentsMost {
		return nil, fmt.Errorf("at most %d attachments at once", attachmentsMost)
	}
	return out, nil
}

// attachmentRules says which paths on the assistant's computer are the user's attachments in conversation self: under
// the node's uploads directory (pictures it saved), in the directory where Claude Code saved this conversation's
// pictures (…/<self>/images/), or a path one of the user's messages carries (a file sent with the app's Files lands in
// the temp directory and its path goes in the message).
type attachmentRules struct {
	home, uploads, self string
	said                func() string
}

func (s *Service) attachmentRules(ctx context.Context, from, self string) attachmentRules {
	r := attachmentRules{self: self}
	if m, ok := s.fleetMachine(from); ok && m.Home != "" {
		r.home, r.uploads = m.Home, stateDirOn(m.Home, m.DataDir, "uploads")+"/"
	}
	var text *string
	r.said = func() string {
		if text == nil {
			t := s.userSaid(ctx, from, self)
			text = &t
		}
		return *text
	}
	return r
}

// resolve is p with ~/ the computer's home, cleaned.
func (r attachmentRules) resolve(p string) string {
	if strings.HasPrefix(p, "~/") && r.home != "" {
		p = proto.ExpandHome(p, r.home)
	}
	return path.Clean(p)
}

// saved: p is where Persona or Claude Code keeps the user's pictures.
func (r attachmentRules) saved(clean string) bool {
	return r.uploads != "" && strings.HasPrefix(clean, r.uploads) || r.self != "" && strings.Contains(clean, "/"+r.self+"/images/")
}

// given: p may be named as an attachment, a picture saved for the user or a path the user wrote.
func (r attachmentRules) given(p string) bool {
	return r.saved(r.resolve(p)) || strings.Contains(r.said(), p)
}

// mentioned: p written in a text is taken as an attachment by itself only where the user's own files land, never any
// path the user named (a file of a repository there would be copied instead of edited where it is).
func (r attachmentRules) mentioned(p string) bool {
	clean := r.resolve(p)
	return r.saved(clean) || appUploadName.MatchString(path.Base(clean)) && strings.Contains(r.said(), p)
}

// checkAttachments resolves the named attachments and refuses one the user did not give, so the tool cannot move any
// file between computers.
func (s *Service) checkAttachments(ctx context.Context, from, self string, paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, nil
	}
	r := s.attachmentRules(ctx, from, self)
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if !r.given(p) {
			return nil, fmt.Errorf("attachment %s is not one the user gave you in this conversation: only a picture or file they sent, or a path they wrote, can be handed to an agent", p)
		}
		out = append(out, r.resolve(p))
	}
	return out, nil
}

// mentionedAttachments are the user's attachments a text names by path that are not among named already.
func (s *Service) mentionedAttachments(ctx context.Context, from, self, text string, named []string) []string {
	var out []string
	var r *attachmentRules
	seen := map[string]bool{}
	for _, p := range named {
		seen[p] = true
	}
	for _, m := range textPath.FindAllStringSubmatch(text, -1) {
		p := strings.TrimRight(m[1], ".:")
		if seen[p] || len(out) >= attachmentsMost {
			continue
		}
		seen[p] = true
		if r == nil {
			rr := s.attachmentRules(ctx, from, self)
			r = &rr
		}
		if r.mentioned(p) {
			out = append(out, p)
		}
	}
	return out
}

// userSaid is the text of the user's messages in conversation self on machine (its recent turns): where the paths of
// the files they sent are.
func (s *Service) userSaid(ctx context.Context, machine, self string) string {
	if machine == "" || self == "" {
		return ""
	}
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, machine, DeviceSessionItems, map[string]any{"session_id": self, "turns": 40, "lazy_images": true}, &res); err != nil {
		s.logf("attachments: read %s on %s: %v", self, machine, err)
		return ""
	}
	var b strings.Builder
	for _, it := range res.Items {
		if it.Role == "user" && it.Text != "" {
			b.WriteString(it.Text)
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// handedAttachments are the attachments as they reach the agent's computer.
type handedAttachments struct {
	Files, Pictures []string          // their paths there
	Missing         []string          // named ones that could not come, with why
	Copied          bool              // copied there from the assistant's computer
	moved           map[string]string // a path on the assistant's computer → its copy's, for the text
}

// handAttachments gets the named paths and those a text mentioned from the assistant's computer (from) to the agent's
// (to): the same paths on the same computer, copies on another. A mentioned one that cannot be copied (a directory,
// one too big) is left as the text has it, unlisted.
func (s *Service) handAttachments(ctx context.Context, from, to string, named, mentioned []string) handedAttachments {
	var h handedAttachments
	same := from == "" || from == to || s.sameMachine(from, to)
	if same {
		for _, p := range named {
			h.add(p, isPicturePath(p))
		}
		return h
	}
	h.Copied = true
	for i, p := range append(append([]string(nil), named...), mentioned...) {
		at, picture, err := s.copyAttachment(ctx, from, to, p)
		if err != nil {
			if i < len(named) {
				h.Missing = append(h.Missing, p+"（"+err.Error()+"）")
			}
			continue
		}
		if h.moved == nil {
			h.moved = map[string]string{}
		}
		h.moved[p] = at
		h.add(at, picture)
	}
	return h
}

func (h *handedAttachments) add(p string, picture bool) {
	if picture {
		h.Pictures = append(h.Pictures, p)
	} else {
		h.Files = append(h.Files, p)
	}
}

// copyAttachment copies p from one computer into the other's uploads/terminal and says where it is and whether it is a
// picture.
func (s *Service) copyAttachment(ctx context.Context, from, to, p string) (string, bool, error) {
	name, mime, data, err := s.readAttachment(ctx, from, p)
	if err != nil {
		return "", false, err
	}
	at, err := s.uploadBytesInto(ctx, to, name, data, "terminal")
	if err != nil {
		return "", false, err
	}
	return at, strings.HasPrefix(mime, "image/") || isPicturePath(p), nil
}

// text is what ends the agent's message: the files, what is missing and the pictures (prompts/assistant/task_attachments).
func (h handedAttachments) text() string {
	if len(h.Files)+len(h.Missing)+len(h.Pictures) == 0 {
		return ""
	}
	return prompts.RenderTaskAttachments(prompts.TaskAttachments{Files: h.Files, Missing: h.Missing, Pictures: h.Pictures, Copied: h.Copied})
}

// said is what the tool's answer adds: how many went and which did not.
func (h handedAttachments) said() string {
	n := len(h.Files) + len(h.Pictures)
	if n == 0 && len(h.Missing) == 0 {
		return ""
	}
	out := fmt.Sprintf(" Attachments: %d handed", n)
	if h.Copied && n > 0 {
		out += " (copied to the agent's computer; the text names the copies)"
	}
	if len(h.Missing) > 0 {
		out += "; not handed: " + strings.Join(h.Missing, "; ")
	}
	return out + "."
}

// withAttachments is text with each copied path named by its copy, ended by the attachments' part.
func withAttachments(text string, h handedAttachments) string {
	for from, to := range h.moved {
		text = strings.ReplaceAll(text, from, to)
	}
	if a := h.text(); a != "" {
		return text + "\n\n" + a
	}
	return text
}

// readAttachment reads a file on machine whole, in file.chunk's pieces (as the hub's downloads read one), refusing one
// over attachmentMostBytes before any of it is read.
func (s *Service) readAttachment(ctx context.Context, machine, p string) (name, mime string, data []byte, err error) {
	var head proto.FileChunk
	if err := s.call(ctx, machine, "file.chunk", proto.FileChunkRequest{Path: p}, &head); err != nil {
		return "", "", nil, err
	}
	if head.Size > attachmentMostBytes {
		return "", "", nil, fmt.Errorf("over %d MB", attachmentMostBytes>>20)
	}
	data = make([]byte, 0, head.Size)
	for int64(len(data)) < head.Size {
		var c proto.FileChunk
		req := proto.FileChunkRequest{Path: p, Offset: int64(len(data)), Length: proto.FileDownloadChunkBytes, Revision: head.Revision}
		if err := s.call(ctx, machine, "file.chunk", req, &c); err != nil {
			return "", "", nil, err
		}
		if len(c.Data) == 0 {
			return "", "", nil, fmt.Errorf("the file ended early")
		}
		data = append(data, c.Data...)
	}
	name = head.Name
	if name == "" {
		name = path.Base(p)
	}
	return name, head.Mime, data, nil
}

// isPicturePath: the file's extension is a picture's (as the node saves them, node/send.go).
func isPicturePath(p string) bool {
	switch strings.ToLower(path.Ext(p)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".heic":
		return true
	}
	return false
}

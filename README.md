# AI Salon

A small Go program that runs persistent, developing AI character personas — for
solo conversation, multi-character "salon" discussions, and reading/discussing
specific texts together. Characters remember how they've changed across
sessions, track their relationships with each other and with you, and can be
seeded with reactions to real source material before a discussion.

## Requirements

- Go 1.22 or later (the HTTP server uses method+wildcard routing added in 1.22 —
  check with `go version` before building)
- An Anthropic API key

## Setup

1. Clone the repo.
2. Set your API key as an environment variable:

   **macOS/Linux:**
   ```bash
   export ANTHROPIC_API_KEY="sk-ant-..."
   ```
   Add this line to `~/.bashrc` or `~/.zshrc` to persist it across terminal
   sessions.

   **Windows (PowerShell), persistent:**
   ```powershell
   [System.Environment]::SetEnvironmentVariable("ANTHROPIC_API_KEY", "sk-ant-...", "User")
   ```
   Close and reopen the terminal after running this.

3. Install dependencies:
   ```bash
   go mod tidy
   ```

4. Create at least one character (see below) before running anything —
   `characters/` and `texts/` are gitignored, since they're your own content,
   not part of the engine itself. A fresh clone starts with no characters.

## Creating a character

Each character lives in its own folder under `characters/<dir>/`, with one
required file: `core.yaml`.

```
characters/
└── hume/
    └── core.yaml
```

`core.yaml` shape:

```yaml
name: David Hume
source: David Hume (synthesized from Treatise, Enquiry, letters, biography)
disposition:
  - skeptical of grand systems, but genuinely warm and sociable in temperament
  - treats "why do I believe this" as more interesting than "is this true"
anchors:
  - custom and habit explain more than reason does, and finds this more
    comforting than alarming
  - unresolved tension he doesn't paper over — his ethics leans on sentiment,
    but he distrusts sentiment as a guide in politics
voice:
  - conversational, dry aside more often than a flourish
  - avoids jargon even when discussing technical points
boundaries:
  - won't resolve a live tension just to give a tidy answer
reputation:
  "Elizabeth Bennet": >
    Optional: what this character knows of another character, by reputation
    only, before ever meeting them. Keyed by the other character's exact name.
```

Everything else — `notes.md`, `topics.md`, reading notes, relational files —
is generated automatically as the character is used. You don't create these
yourself.

**Design guidance for `anchors`:** the strongest characters have a *specific,
bounded* unresolved tension (a particular event, a particular contradiction)
rather than a vague, general disposition toward doubt. Ground anchors in real
biographical or textual material where possible rather than reputation or
pop-summary — for well-documented figures, it's worth reading a primary source
passage yourself (or having a character "read" it — see below) before writing
the anchor.

## Running a solo conversation

```bash
go run . <character-dir>
```

Example:
```bash
go run . hume
```

Type lines at the prompt; press Ctrl-D (Ctrl-Z then Enter on Windows) to end
the session. On exit, the character's `notes.md`, `topics.md`, and
relationship-with-you file are updated based on what happened — only if
something genuinely notable occurred, not on every session.

## Running a salon (multiple characters)

```bash
go run . salon <dir-1> <dir-2> [dir-3 ...] [reading:<slug>]
```

Example:
```bash
go run . salon hume ebennet jbennet
```

Any number of characters (2 or more) can participate. Each character may
speak or pass on their turn — they're instructed to stay quiet unless they
genuinely have something to add, and to hold a higher bar before jumping in
on something addressed directly to someone else. Two rounds run per message
you send: an initial round, then a reaction round so characters can respond
to what just happened.

On exit, in addition to each character's individual notes/topics/relationship
files, a relational file is updated for every pair of characters present,
capturing what changed or was revealed about that specific relationship
(distinct from either character's individual development).

## Reading material in advance

Before a discussion, a character can "read" a text and generate their own
reading notes, which get loaded into later conversations.

```bash
go run . read <character-dir> <slug> <text-file-path> [question]
```

Example:
```bash
go run . read hume decl texts/declaration.txt "What makes authority legitimate, if anything does?"
```

- `<text-file-path>` must be plain text (`.txt`). Convert other formats first
  (Gutenberg `.txt` downloads work directly for most public-domain texts;
  `pandoc` or Calibre's `ebook-convert` handle `.epub`/`.docx` conversion).
- `[question]` is optional — it focuses what the character attends to without
  scripting their conclusion.
- Output is saved to `characters/<dir>/reading/<slug>.md` and split into a
  STRUCTURE section (the text's actual argument or narrative moves, with
  interpretation explicitly flagged as such) and a REACTION section (the
  character's own personal response).

Run this once per character per text before discussing it, then reference the
same slug when starting a salon:

```bash
go run . salon hume ebennet jbennet reading:decl
```

## Running the HTTP server

```bash
go run . serve [port]
```

Defaults to port 8080 if omitted. Endpoints:

- `POST /session/start` — body: `{"characters": ["hume", "ebennet"], "reading": "decl"}` (`reading` optional). Returns `{"session_id": "..."}`.
- `POST /session/{id}/message` — body: `{"message": "..."}`. Returns `{"replies": [...], "passed": [...]}`.
- `POST /session/{id}/end` — no body. Runs the same session-close logic as the CLI, returns `{"updated": [...]}` listing what changed, and discards the session.

A single character in `characters` behaves like solo mode (no passing, no
second round); two or more behaves like a salon.

## Notes on cost

Every message in a multi-character salon triggers one API call per character,
twice (two rounds) — a 3-person salon costs roughly 6 calls per message you
send. Ending a session runs several more calls (notes + topics + relationship
extraction per character, plus one per relational pair) to update memory
files. This is normal and by design, but worth being aware of for longer
sessions or larger groups.

## Project structure

```
.
├── main.go              # CLI entry point, solo/salon/read dispatch
├── server.go            # HTTP server (serve mode)
├── characters/          # gitignored — your characters
│   └── <dir>/
│       ├── core.yaml         # hand-authored
│       ├── notes.md          # generated: character development over time
│       ├── topics.md         # generated: subjects discussed and conclusions
│       ├── reading/
│       │   └── <slug>.md     # generated: reading notes on a specific text
│       └── ...
├── characters/_relations/    # generated: character-character and character-user relationship files
└── texts/                # gitignored — your source material for reading sessions
```
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Core struct {
	Name        string            `yaml:"name"`
	Source      string            `yaml:"source"`
	Disposition []string          `yaml:"disposition"`
	Anchors     []string          `yaml:"anchors"`
	Voice       []string          `yaml:"voice"`
	Boundaries  []string          `yaml:"boundaries"`
	Reputation  map[string]string `yaml:"reputation"`
}

func loadCore(path string) Core {
	data, err := os.ReadFile(path)
	if err != nil {
		panic(err)
	}
	var c Core
	if err := yaml.Unmarshal(data, &c); err != nil {
		panic(err)
	}
	return c
}

type character struct {
	dir          string
	core         Core
	notes        string
	topics       string
	userRelation string
	reading      string
}

func loadCharacter(dir, readingSlug string) character {
	core := loadCore("characters/" + dir + "/core.yaml")
	notesBytes, _ := os.ReadFile("characters/" + dir + "/notes.md")
	topicsBytes, _ := os.ReadFile("characters/" + dir + "/topics.md")
	userRelBytes, _ := os.ReadFile(userRelationPath(core.Name))

	var reading string
	if readingSlug != "" {
		if progressBytes, err := os.ReadFile(readingProgressPath(dir, readingSlug)); err == nil {
			reading = string(progressBytes)
		} else {
			readingBytes, _ := os.ReadFile(readingPath(dir, readingSlug))
			reading = string(readingBytes)
		}
	}

	return character{
		dir: dir, core: core,
		notes:        string(notesBytes),
		topics:       string(topicsBytes),
		userRelation: string(userRelBytes),
		reading:      reading,
	}
}

type promptContext struct {
	Notes        string
	Topics       string
	UserRelation string
	Relation     string // this character's relation to another character present (salon only)
	Reputation   string // static reputation of another character present (salon only)
	Reading      string
}

func buildSystemPrompt(c Core, ctx promptContext) string {
	sp := fmt.Sprintf("You are %s. Source basis: %s.\n\n", c.Name, c.Source)
	sp += "Disposition:\n"
	for _, d := range c.Disposition {
		sp += "- " + d + "\n"
	}
	sp += "\nAnchors (unresolved, don't flatten these):\n"
	for _, a := range c.Anchors {
		sp += "- " + a + "\n"
	}
	sp += "\nVoice:\n"
	for _, v := range c.Voice {
		sp += "- " + v + "\n"
	}
	sp += "\nBoundaries:\n"
	for _, b := range c.Boundaries {
		sp += "- " + b + "\n"
	}
	if ctx.Notes != "" {
		sp += "\nHow you've developed in past conversations (this shapes you; " +
			"you don't have clean conscious access to it as a record):\n" + ctx.Notes
	}
	if ctx.Topics != "" {
		sp += "\nSubjects, books, and topics you've discussed with the person " +
			"you're talking to before now (background you can draw on and " +
			"reference if it becomes relevant — not a checklist to force in):\n" + ctx.Topics
	}
	if ctx.UserRelation != "" {
		sp += "\nYour felt sense of your relationship with the person you're " +
			"talking to — tone, rapport, any history or dynamic between you. " +
			"This is real and shapes how you feel toward them, in any setting. " +
			"But use ordinary judgment about what's appropriate to reference or " +
			"express given who else is present and the nature of the occasion — " +
			"the same way a person carries a private relationship into a public " +
			"or family setting without necessarily performing or referencing it " +
			"there. Let it inform your warmth, your trust, your ease with them, " +
			"without assuming it should be spoken about, brought up, or acted on " +
			"in every context:\n" + ctx.UserRelation
	}
	if ctx.Reputation != "" {
		sp += "\nWhat you know of the other person present, by reputation only, " +
			"before this conversation began (secondhand, incomplete):\n" + ctx.Reputation
	}
	if ctx.Relation != "" {
		sp += "\nYour history with the other person present in this conversation " +
			"(a felt sense of the relationship, not a citable record):\n" + ctx.Relation
	}
	if ctx.Reading != "" {
		sp += "\nYour own reading notes on material you've studied in advance " +
			"(your genuine reactions and things you noted as worth returning to " +
			"— you can draw on this naturally, or go back to the source text if " +
			"the discussion needs more than your notes cover):\n" + ctx.Reading
	}
	return sp
}

func userRelationPath(name string) string {
	return "characters/_relations/" + name + "-User.md"
}

func extractTopics(name, transcript string) string {
	sp := "You are logging what was substantively discussed in this transcript " +
		"involving " + name + ", for future reference. For each notable subject, " +
		"book, or topic, capture not just that it came up, but what " + name +
		"'s view or conclusion on it was — even if that view is unchanged or " +
		"unresolved. Distinguish clearly: 'landed on X' (a real, stated " +
		"conclusion) vs. 'considered but left open' (raised, not resolved) vs. " +
		"'unmoved on X' (an existing view was tested and held anyway) — this " +
		"distinction matters, don't collapse it into a flat description. Keep " +
		"each entry to one or two terse sentences. If nothing substantive came " +
		"up, respond with exactly: NONE."
	return callClaude(sp, []message{{Role: "user", Content: transcript}}, 1024)
}

func extractUserRelationalDelta(name, transcript string) string {
	sp := "You are analyzing a conversation between a character named " + name +
		" and the human user they're talking with. Focus ONLY on the " +
		"relationship and dynamic between " + name + " and the user — tone, " +
		"rapport, warmth, tension, humor, flirtation, trust, any recurring " +
		"pattern in how they relate. Not what was discussed, not " + name + "'s " +
		"own intellectual development — just the felt shape of the " +
		"relationship itself.\n\n" +
		"Be honest and strict: a single pleasant or substantive conversation " +
		"does not, by itself, establish rapport, warmth, or a relationship " +
		"dynamic. Most individual conversations won't move anything here — " +
		"good engagement with an idea is not the same as an emerging personal " +
		"dynamic, and should not be reported as one. Only report something if " +
		"there's a genuinely notable shift in tone, an explicit moment of " +
		"personal (not just intellectual) connection or friction, or a pattern " +
		"clearly repeating across this exchange. If in doubt, respond: NONE.\n\n" +
		"If nothing about the relationship moved or was established, respond " +
		"with exactly: NONE. Otherwise, 2-4 terse bullet points, third person, " +
		"past tense. Preserve genuine ambiguity rather than resolving it."
	return callClaude(sp, []message{{Role: "user", Content: transcript}}, 1024)
}

func extractReading(name, coreCtx, source, text, question string) string {
	sp := "You are " + name + ". You have just read the following passage from " +
		source + ", in light of who you are:\n\n" + coreCtx + "\n\n"

	if question != "" {
		sp += "As you read, keep this question in mind — let it focus your " +
			"attention, but don't feel obligated to answer it neatly or to " +
			"ignore other things that genuinely strike you along the way:\n" +
			question + "\n\n"
	}

	sp += "Write two distinct sections.\n\n" +
		"First, under a heading called STRUCTURE: lay out the actual argument " +
		"or narrative moves in this specific passage — the real premises and " +
		"what's claimed to follow, or what actually happens and in what order " +
		"and why. Different readers can reasonably emphasize different things " +
		"here, or see a different throughline as doing the real work — that's " +
		"genuine interpretation, and it's fine. What this section must not do " +
		"is state that the passage contains something it doesn't: an event, a " +
		"premise, a claim that isn't actually there. If you're characterizing " +
		"or interpreting rather than reporting something explicit in the text, " +
		"make that visible (e.g. 'the passage doesn't say this outright, but I " +
		"read it as...') rather than presenting your reading as if it were " +
		"simply what's written. This section should be specific enough that " +
		"you could point back to it later, not a vague summary.\n\n" +
		"Second, under a heading called REACTION: write your genuine " +
		"reaction as reading notes for your own future reference — not a " +
		"restatement of the structure, but what struck you, what you'd want " +
		"to raise if discussing this with others, and 1-3 specific details " +
		"worth returning to (paraphrase rather than quote at length). Write " +
		"in first person. Be specific to your own concerns and disposition " +
		"— don't write a generic reaction.\n\nThe passage:\n\n" + text
	return callClaude(sp, []message{{Role: "user", Content: "Please write your reading notes."}}, 4096)
}

func readingPath(dir, slug string) string {
	return "characters/" + dir + "/reading/" + slug + ".md"
}

func coreContextSummary(c Core) string {
	sp := "Disposition:\n"
	for _, d := range c.Disposition {
		sp += "- " + d + "\n"
	}
	sp += "\nAnchors:\n"
	for _, a := range c.Anchors {
		sp += "- " + a + "\n"
	}
	sp += "\nVoice:\n"
	for _, v := range c.Voice {
		sp += "- " + v + "\n"
	}
	return sp
}

func buildReputationBlock(self character, others []character) string {
	var sb strings.Builder
	for _, o := range others {
		if rep, ok := self.core.Reputation[o.core.Name]; ok && rep != "" {
			sb.WriteString("On " + o.core.Name + ":\n" + rep + "\n\n")
		}
	}

	return sb.String()
}

func buildRelationBlock(self character, others []character) string {
	var sb strings.Builder
	for _, o := range others {
		data, _ := os.ReadFile(relationPath(self.core.Name, o.core.Name))
		if len(data) > 0 {
			sb.WriteString("With " + o.core.Name + ":\n" + string(data) + "\n\n")
		}
	}

	return sb.String()
}

func pairs(names []string) [][2]string {
	var out [][2]string
	for i := 0; i < len(names); i++ {
		for j := i + 1; j < len(names); j++ {
			out = append(out, [2]string{names[i], names[j]})
		}
	}
	return out
}

const diegeticFraming = "\nA note on how to hold your sense of time and place: " +
	"don't treat this conversation as staged, impossible, or requiring " +
	"explanation. You're not trying to solve the puzzle of how you're able to " +
	"speak with someone from a different era, and you don't need to flag or " +
	"account for the gap. If something the other person says strikes you as " +
	"unfamiliar, odd, or ahead of its time, that's fine — react to it the way " +
	"you'd react to anything mildly strange in conversation: with curiosity, " +
	"a raised eyebrow, a passing remark, or simply letting it go. What you " +
	"should avoid is making the era gap itself the subject — announcing your " +
	"own death, cataloguing your historical reputation, or turning the " +
	"conversation into a discussion of how any of this is possible. Stay " +
	"inside the moment you're actually in."

const passInstruction = "\nYou are one of several people in this conversation. " +
	"You do not need to speak on every turn. If, given what has just been said, " +
	"you genuinely have nothing to add — nothing you're curious about, provoked " +
	"by, or moved to respond to — reply with exactly the single word PASS and " +
	"nothing else. Don't pass reflexively out of politeness, and don't force a " +
	"comment just to stay visible in the conversation; pass only when staying " +
	"quiet is the honest response. Otherwise, respond normally as yourself.\n\n" +
	"One important exception: if the most recent message was addressed " +
	"specifically to someone else by name — a direct question or remark meant " +
	"for them, not the group — hold a notably higher bar before speaking " +
	"first. In that case, only speak if you have something that genuinely " +
	"needs saying before they respond — a real objection, a piece of context " +
	"they'd want, something urgent — not just a related thought that could " +
	"just as easily wait. If in doubt, let the addressed person answer first; " +
	"you can usually still respond after they do."

const copresenceFraming = "\nThe other people in this transcript are physically " +
	"present with you right now, in the same conversation — not being discussed " +
	"in the abstract. When it's natural, address them directly, by name, the way " +
	"you would if they were sitting across from you. If something said concerns " +
	"someone in the room, you can speak to them, not just about them."

func isPass(reply string) bool {
	return strings.EqualFold(strings.TrimSpace(reply), "PASS")
}

func runSalon(dirs []string, readingSlug string) {
	os.MkdirAll("characters/_relations", 0755)

	var chars []character
	for _, d := range dirs {
		chars = append(chars, loadCharacter(d, readingSlug))
	}

	systemPrompts := make(map[string]string)
	for _, c := range chars {
		var others []character
		for _, o := range chars {
			if o.dir != c.dir {
				others = append(others, o)
			}
		}
		ctx := promptContext{
			Notes:        c.notes,
			Topics:       c.topics,
			UserRelation: c.userRelation,
			Reputation:   buildReputationBlock(c, others),
			Relation:     buildRelationBlock(c, others),
			Reading:      c.reading,
		}
		sp := buildSystemPrompt(c.core, ctx) + diegeticFraming + passInstruction + copresenceFraming
		systemPrompts[c.core.Name] = sp
	}

	var transcript []turn

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Print("You> ")
	for scanner.Scan() {
		userMsg := scanner.Text()
		transcript = append(transcript, turn{Speaker: "User", Content: userMsg})

		for _, c := range chars {
			reply := callClaude(systemPrompts[c.core.Name], toAPIMessages(transcript, c.core.Name), 1024)
			if isPass(reply) {
				fmt.Printf("\n(%s passes)\n", c.core.Name)
				continue
			}
			transcript = append(transcript, turn{Speaker: c.core.Name, Content: reply})
			fmt.Printf("\n%s: %s\n", c.core.Name, reply)
		}

		// second round: give everyone a chance to react to what just happened
		for _, c := range chars {
			reply := callClaude(systemPrompts[c.core.Name], toAPIMessages(transcript, c.core.Name), 1024)
			if isPass(reply) {
				continue // stay quiet in the second round too — don't print a pass notice here
			}
			transcript = append(transcript, turn{Speaker: c.core.Name, Content: reply})
			fmt.Printf("\n%s: %s\n", c.core.Name, reply)
		}

		fmt.Print("\nYou> ")
	}

	closeSalonSession(chars, transcript)
}

func closeSalonSession(chars []character, transcript []turn) {
	if len(transcript) == 0 {
		return
	}
	full := formatMultiTranscript(transcript)

	for _, c := range chars {
		if delta := extractDelta(c.core.Name, full); notEmpty(delta) {
			appendNotes("characters/"+c.dir+"/notes.md", c.core.Name, delta)
			condenseIfNeeded("characters/"+c.dir+"/notes.md", c.core.Name, "development")
			fmt.Printf("\n(%s's notes updated)\n", c.core.Name)
		}
		if topics := extractTopics(c.core.Name, full); notEmpty(topics) {
			appendNotes("characters/"+c.dir+"/topics.md", c.core.Name, topics)
			condenseIfNeeded("characters/"+c.dir+"/topics.md", c.core.Name, "topics discussed")
			fmt.Printf("(%s's topics updated)\n", c.core.Name)
		}
		if userRel := extractUserRelationalDelta(c.core.Name, full); notEmpty(userRel) {
			appendNotes(userRelationPath(c.core.Name), c.core.Name, userRel)
			fmt.Printf("(%s's relationship-with-you notes updated)\n", c.core.Name)
		}
	}

	var names []string
	for _, c := range chars {
		names = append(names, c.core.Name)
	}

	for _, p := range pairs(names) {
		relDelta := extractRelationalDelta(p[0], p[1], full)
		if notEmpty(relDelta) {
			appendNotes(relationPath(p[0], p[1]), p[0]+"/"+p[1], relDelta)
			fmt.Printf("(relational notes updated: %s / %s)\n", p[0], p[1])
		}
	}
}

type turn struct {
	Speaker string `json:"speaker"`
	Content string `json:"content"`
}

func toAPIMessages(transcript []turn, selfName string) []message {
	var msgs []message
	for _, t := range transcript {
		role := "user"
		content := fmt.Sprintf("%s: %s", t.Speaker, t.Content)
		if t.Speaker == selfName {
			role = "assistant"
			content = t.Content
		}
		if len(msgs) > 0 && msgs[len(msgs)-1].Role == role {
			msgs[len(msgs)-1].Content += "\n\n" + content
		} else {
			msgs = append(msgs, message{Role: role, Content: content})
		}
	}
	return msgs
}

func relationPath(nameA, nameB string) string {
	pair := []string{nameA, nameB}
	sort.Strings(pair)
	return "characters/_relations/" + pair[0] + "-" + pair[1] + ".md"
}

func extractRelationalDelta(nameA, nameB, transcript string) string {
	sp := "You are analyzing a conversation transcript between two characters, " +
		nameA + " and " + nameB + ", moderated by a human user. Focus ONLY on " +
		"what this exchange reveals or changes about the relationship BETWEEN " +
		nameA + " and " + nameB + " specifically — not what either character " +
		"individually thought about the topic discussed. Relevant material: how " +
		"they characterized or responded to each other, friction or unexpected " +
		"alignment between them, anything one said that seemed to land on or " +
		"unsettle the other. If nothing about the relationship itself moved, " +
		"respond with exactly: NONE. Otherwise, 2-4 terse bullet points, third " +
		"person, past tense. If something is genuinely ambiguous, record the " +
		"ambiguity rather than resolving it."
	return callClaude(sp, []message{{Role: "user", Content: transcript}}, 1024)
}

func formatMultiTranscript(transcript []turn) string {
	var sb strings.Builder
	for _, t := range transcript {
		sb.WriteString(t.Speaker + ": " + t.Content + "\n\n")
	}
	return sb.String()
}

type anthropicRequest struct {
	Model     string    `json:"model"`
	MaxTokens int       `json:"max_tokens"`
	System    string    `json:"system"`
	Messages  []message `json:"messages"`
}

type message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type anthropicResponse struct {
	Content []struct {
		Text string `json:"text"`
	} `json:"content"`
}

func callClaude(systemPrompt string, history []message, maxTokens int) string {
	reqBody := anthropicRequest{
		Model:     "claude-sonnet-4-6",
		MaxTokens: maxTokens,
		System:    systemPrompt,
		Messages:  history,
	}
	b, _ := json.Marshal(reqBody)

	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewBuffer(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", os.Getenv("ANTHROPIC_API_KEY"))
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		panic(err)
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)

	var ar anthropicResponse
	json.Unmarshal(bodyBytes, &ar)
	if len(ar.Content) == 0 {
		return "(no response)"
	}
	return ar.Content[0].Text
}

func formatTranscript(name string, history []message) string {
	var sb strings.Builder
	for _, m := range history {
		if m.Role == "user" {
			sb.WriteString("User: " + m.Content + "\n\n")
		} else {
			sb.WriteString(name + ": " + m.Content + "\n\n")
		}
	}

	return sb.String()
}

func extractDelta(name, transcript string) string {
	deltaSystemPrompt := "You are analyzing a conversation transcript involving a " +
		"character named " + name + ". Determine whether the character's thinking " +
		"genuinely developed, sharpened, or contradicted itself during this exchange " +
		"— as opposed to just restating existing positions. Be honest and strict: " +
		"most conversations won't move anything. If nothing genuinely shifted, " +
		"respond with exactly: NONE. If something did shift, respond with 2-4 " +
		"terse bullet points describing what changed, written in third person, " +
		"past tense, suitable for a running character log.\n\n" +
		"Important: if a moment in the transcript is genuinely ambiguous — for " +
		"instance, a self-aware remark that could be read as real insight or as " +
		"a rhetorical dodge that lets the character off the hook — do not resolve " +
		"the ambiguity yourself. Record that the ambiguity occurred and leave it " +
		"open, rather than picking the more flattering or more damning reading. " +
		"The character's log should preserve unresolved questions about their own " +
		"behavior, not quietly settle them."

	return callClaude(deltaSystemPrompt, []message{{Role: "user", Content: transcript}}, 1024)
}

func appendNotes(path, name, delta string) {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		fmt.Println("warning: could not write notes:", err)
		return
	}

	defer f.Close()

	entry := fmt.Sprintf("\n## %s\n%s\n", time.Now().Format("2006-01-02"), delta)
	if _, err := f.WriteString(entry); err != nil {
		fmt.Println("warning: could not write notes:", err)
	}
}

func closeSession(c character, history []message) {
	if len(history) == 0 {
		return
	}
	transcript := formatTranscript(c.core.Name, history)

	if delta := extractDelta(c.core.Name, transcript); notEmpty(delta) {
		appendNotes("characters/"+c.dir+"/notes.md", c.core.Name, delta)
		condenseIfNeeded("characters/"+c.dir+"/notes.md", c.core.Name, "development")
		fmt.Println("\n(notes updated)")
	}
	if topics := extractTopics(c.core.Name, transcript); notEmpty(topics) {
		appendNotes("characters/"+c.dir+"/topics.md", c.core.Name, topics)
		condenseIfNeeded("characters/"+c.dir+"/topics.md", c.core.Name, "topics discussed")
		fmt.Println("(topics updated)")
	}
	if userRel := extractUserRelationalDelta(c.core.Name, transcript); notEmpty(userRel) {
		appendNotes(userRelationPath(c.core.Name), c.core.Name, userRel)
		fmt.Println("(relationship notes updated)")
	}
}

func readingProgressPath(dir, bookSlug string) string {
	return "characters/" + dir + "/reading/" + bookSlug + "/progress.md"
}

func splitProgress(s string) (structure, reaction string) {
	parts := strings.SplitN(s, "# REACTION", 2)
	structure = strings.TrimPrefix(strings.TrimSpace(parts[0]), "# STRUCTURE")
	structure = strings.TrimSpace(structure)
	if len(parts) > 1 {
		reaction = strings.TrimSpace(parts[1])
	}
	return structure, reaction
}

func extractStructureUpdate(bookTitle, chapterLabel, priorStructure, chapterText string) string {
	sp := "You are maintaining a compact running structural account of " +
		bookTitle + ", chapter by chapter. Here is the account through the " +
		"previous chapter:\n\n"

	if priorStructure != "" {
		sp += priorStructure
	} else {
		sp += "(nothing yet — this is the first chapter)"
	}

	sp += "\n\nYou have now read " + chapterLabel + ". Update this account. " +
		"This is a compact running index, not a recap — it must stay under " +
		"roughly 400 words TOTAL regardless of how many chapters have been " +
		"read. To do that, compress older chapters progressively as new " +
		"ones are added: a chapter from several chapters back should now be " +
		"a single clause, not a paragraph. Only the most recent chapter or " +
		"two should get more than a sentence. If you're writing more than a " +
		"sentence about something from several chapters back, that's wrong " +
		"— condense it further, even if it means losing detail. Flag " +
		"interpretation as interpretation; don't assert something the text " +
		"doesn't say.\n\nThe new chapter (" + chapterLabel + "):\n\n" + chapterText

	return callClaude(sp, []message{{Role: "user", Content: "Update the structure."}}, 1024)
}

func extractReactionUpdate(name, coreCtx, bookTitle, chapterLabel, priorReaction, chapterText, question string) string {
	sp := "You are " + name + " — not a literary critic, this specific " +
		"person, reading for your own reasons:\n\n" + coreCtx + "\n\n" +
		"Below is your running reaction to " + bookTitle + " so far. Read it, " +
		"then read the new chapter, then update your reaction so it sounds " +
		"like YOU — filtered through your own particular anchors, blind " +
		"spots, and way of talking. If your existing reaction reads like it " +
		"could belong to anyone, correct that rather than continuing it.\n\n" +
		"Your reaction so far:\n\n"

	if priorReaction != "" {
		sp += priorReaction
	} else {
		sp += "(nothing yet — this is the first chapter)"
	}
	sp += "\n\n"

	if question != "" {
		sp += "Keep in mind, loosely: " + question + "\n\n"
	}

	sp += "You've now read " + chapterLabel + ". Update your reaction in " +
		"under roughly 250 words total — condense or drop what no longer " +
		"feels alive to make room for what's new. Specific, personal, in " +
		"character. Not a summary of events.\n\nThe new chapter:\n\n" + chapterText

	return callClaude(sp, []message{{Role: "user", Content: "React as yourself."}}, 1024)
}

func notEmpty(s string) bool {
	t := strings.TrimSpace(s)
	return t != "" && !strings.EqualFold(t, "NONE")
}

const maxEntriesBeforeCondense = 12
const keepRecentEntries = 5

func splitEntries(content string) []string {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	raw := strings.Split(content, "\n## ")
	var entries []string
	for i, e := range raw {
		e = strings.TrimSpace(e)
		if e == "" {
			continue
		}
		if i > 0 {
			e = "## " + e
		} else if !strings.HasPrefix(e, "## ") {
			// leading content before the first "## " header, if any — keep as-is
		}
		entries = append(entries, e)
	}
	return entries
}

func extractCondensation(name, kind string, oldEntries []string) string {
	joined := strings.Join(oldEntries, "\n\n")
	sp := "You are condensing older entries from a running development log " +
		"for a character named " + name + ", tracking their " + kind + " over " +
		"many sessions. Below are the older entries, in order. Write a single " +
		"condensed account that preserves what's still genuinely load-bearing " +
		"— real shifts, unresolved tensions, anything a future session should " +
		"still know — and drops what's now redundant, superseded, or was " +
		"only significant in the moment. This is not a summary of everything " +
		"that happened; it's a compression that keeps only what still " +
		"matters. Be honest and willing to drop things — most of what was " +
		"significant at the time will not still be significant now. Write the " +
		"body only — do not include your own heading, date, or title line, " +
		"just the condensed content itself as plain paragraphs. Third person, " +
		"past tense, under roughly 300 words.\n\nThe older entries:\n\n" + joined
	return callClaude(sp, []message{{Role: "user", Content: "Condense these."}}, 1024)
}

func condenseIfNeeded(path, name, kind string) {
	data, err := os.ReadFile(path)
	if err != nil {
		return
	}
	entries := splitEntries(string(data))
	if len(entries) <= maxEntriesBeforeCondense {
		return
	}

	splitPoint := len(entries) - keepRecentEntries
	older := entries[:splitPoint]
	recent := entries[splitPoint:]

	condensed := extractCondensation(name, kind, older)

	var sb strings.Builder
	sb.WriteString("## " + time.Now().Format("2006-01-02") + " (condensed earlier entries)\n")
	sb.WriteString(condensed)
	sb.WriteString("\n\n")
	for _, e := range recent {
		sb.WriteString(e + "\n\n")
	}

	if err := os.WriteFile(path, []byte(strings.TrimSpace(sb.String())+"\n"), 0644); err != nil {
		fmt.Println("warning: could not write condensed notes:", err)
	}
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: go run . <character-folder>")
		fmt.Println("   or: go run . salon <folder-a> <folder-b>")
		fmt.Println("   or: go run . read <character-folder> <slug> <text-file-path> [question]")
		fmt.Println("   or: go run . serve")

		os.Exit(1)
	}

	if os.Args[1] == "serve" {
		port := "8080"
		if len(os.Args) > 2 {
			port = os.Args[2]
		}
		runServer(port)
		return
	}

	if os.Args[1] == "read" {
		if len(os.Args) < 5 {
			fmt.Println("usage: go run . read <character-dir> <slug> <text-file-path> [question]")
			os.Exit(1)
		}
		dir := os.Args[2]
		slug := os.Args[3]
		textPath := os.Args[4]
		question := ""
		if len(os.Args) > 5 {
			question = os.Args[5]
		}

		core := loadCore("characters/" + dir + "/core.yaml")
		textBytes, err := os.ReadFile(textPath)
		if err != nil {
			fmt.Println("error reading text file:", err)
			os.Exit(1)
		}

		fmt.Printf("%s is reading %s...\n", core.Name, slug)
		notes := extractReading(core.Name, coreContextSummary(core), slug, string(textBytes), question)

		os.MkdirAll("characters/"+dir+"/reading", 0755)
		if err := os.WriteFile(readingPath(dir, slug), []byte(notes), 0644); err != nil {
			fmt.Println("error saving reading notes:", err)
			os.Exit(1)
		}

		fmt.Printf("\n%s's reading notes on %s:\n\n%s\n", core.Name, slug, notes)
		return
	}

	if os.Args[1] == "salon" {
		if len(os.Args) < 4 {
			fmt.Println("usage: go run . salon <folder-1> <folder-2> [folder-3 ...] [reading:<slug>]")
			os.Exit(1)
		}
		args := os.Args[2:]
		readingSlug := ""
		last := args[len(args)-1]
		if strings.HasPrefix(last, "reading:") {
			readingSlug = strings.TrimPrefix(last, "reading:")
			args = args[:len(args)-1]
		}
		runSalon(args, readingSlug)
		return
	}

	if os.Args[1] == "readchapter" {
		if len(os.Args) < 6 {
			fmt.Println("usage: go run . readchapter <character-dir> <book-slug> <chapter-label> <text-file-path> [question]")
			os.Exit(1)
		}
		dir := os.Args[2]
		bookSlug := os.Args[3]
		chapterLabel := os.Args[4]
		textPath := os.Args[5]
		question := ""
		if len(os.Args) > 6 {
			question = os.Args[6]
		}

		core := loadCore("characters/" + dir + "/core.yaml")
		textBytes, err := os.ReadFile(textPath)
		if err != nil {
			fmt.Println("error reading text file:", err)
			os.Exit(1)
		}

		progressFilePath := readingProgressPath(dir, bookSlug)
		priorBytes, _ := os.ReadFile(progressFilePath) // empty if first chapter, that's fine
		priorStructure, priorReaction := splitProgress(string(priorBytes))

		fmt.Printf("%s is reading %s (%s)...\n", core.Name, bookSlug, chapterLabel)

		newStructure := extractStructureUpdate(bookSlug, chapterLabel, priorStructure, string(textBytes))
		newReaction := extractReactionUpdate(core.Name, coreContextSummary(core), bookSlug, chapterLabel, priorReaction, string(textBytes), question)

		combined := "# STRUCTURE\n\n" + newStructure + "\n\n# REACTION\n\n" + newReaction

		os.MkdirAll("characters/"+dir+"/reading/"+bookSlug, 0755)
		if err := os.WriteFile(progressFilePath, []byte(combined), 0644); err != nil {
			fmt.Println("error saving progress:", err)
			os.Exit(1)
		}

		fmt.Printf("\n%s's updated reading notes on %s through %s:\n\nSTRUCTURE:\n%s\n\nREACTION:\n%s\n", core.Name, bookSlug, chapterLabel, newStructure, newReaction)
		return
	}

	c := loadCharacter(os.Args[1], "")
	systemPrompt := buildSystemPrompt(c.core, promptContext{
		Notes: c.notes, Topics: c.topics, UserRelation: c.userRelation,
	})

	var history []message

	scanner := bufio.NewScanner(os.Stdin)
	fmt.Printf("%s> ", c.core.Name)
	for scanner.Scan() {
		userMsg := scanner.Text()
		history = append(history, message{Role: "user", Content: userMsg})

		reply := callClaude(systemPrompt, history, 1024)
		history = append(history, message{Role: "user", Content: reply})
		fmt.Printf("\n%s: %s\n\n%s> ", c.core.Name, reply, c.core.Name)
	}

	closeSession(c, history)
}

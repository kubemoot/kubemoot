/*
Copyright 2026.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0
*/

// Package omp writes a crew's memory in the Open Memory Protocol directory
// format (the agent memory draft contributed by Letta, called Packer in the
// protocol repository): a memory directory with a MEMORY.md at every level,
// where the root files are always in context and nested files are read on demand.
//
// Mapping: each fact becomes <topic>/<key>.md. The topic directory's MEMORY.md
// lists its facts, and the root MEMORY.md lists the topics. The directory format
// has no field for the crew, the namespace, the writing agent or timestamps, so
// those travel in the optional front-matter "metadata" of each fact file.
package omp

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"sigs.k8s.io/yaml"

	"github.com/kubemoot/kubemoot/operator/internal/crewmemory"
	"github.com/kubemoot/kubemoot/operator/internal/crewscope"
)

// IndexFile is the file every OMP memory directory level must contain.
const IndexFile = "MEMORY.md"

// summaryChars bounds the value text shown in an index line.
const summaryChars = 100

// Tree maps a slash-separated path relative to the memory root to file content.
type Tree map[string]string

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9_=-]`)

// factMeta is the Kubemoot metadata carried in each fact file's front matter.
type factMeta struct {
	Namespace string `json:"namespace"`
	Crew      string `json:"crew"`
	Topic     string `json:"topic"`
	Key       string `json:"key"`
	LearnedBy string `json:"learnedBy,omitempty"`
	LearnedAt string `json:"learnedAt,omitempty"`
	UsedAt    string `json:"usedAt,omitempty"`
	Source    string `json:"source"`
}

type frontMatter struct {
	Description string   `json:"description"`
	Metadata    factMeta `json:"metadata"`
}

type indexMatter struct {
	Description string `json:"description"`
}

// entry is a fact placed at a safe path.
type entry struct {
	fact  crewmemory.Fact
	topic string // directory name
	file  string // file name without extension
}

// Build maps one crew's facts to an OMP memory tree. The facts may come in any
// order; the tree is deterministic.
func Build(scope crewscope.Scope, facts []crewmemory.Fact) (Tree, error) {
	byTopic := place(facts)
	tree := Tree{}
	topics := sortedTopics(byTopic)
	for _, topic := range topics {
		for _, e := range byTopic[topic] {
			doc, err := factDocument(e)
			if err != nil {
				return nil, err
			}
			tree[topic+"/"+e.file+".md"] = doc
		}
		idx, err := topicIndex(topic, byTopic[topic])
		if err != nil {
			return nil, err
		}
		tree[topic+"/"+IndexFile] = idx
	}
	root, err := rootIndex(scope, topics, byTopic)
	if err != nil {
		return nil, err
	}
	tree[IndexFile] = root
	return tree, nil
}

// place groups facts by topic directory and gives each a file name that is safe
// on disk, never the index name, and unique ignoring case.
func place(facts []crewmemory.Fact) map[string][]entry {
	byTopic := map[string][]entry{}
	taken := map[string]map[string]bool{}
	dirs := map[string]string{} // lower-case directory name -> the spelling used
	for _, f := range sortedFacts(facts) {
		topic := safeName(f.Topic)
		if first, ok := dirs[strings.ToLower(topic)]; ok {
			topic = first
		} else {
			dirs[strings.ToLower(topic)] = topic
		}
		if taken[topic] == nil {
			taken[topic] = map[string]bool{}
		}
		file := uniqueName(safeName(f.Key), taken[topic])
		byTopic[topic] = append(byTopic[topic], entry{fact: f, topic: topic, file: file})
	}
	for _, es := range byTopic {
		sort.Slice(es, func(i, j int) bool { return es[i].file < es[j].file })
	}
	return byTopic
}

// safeName reduces a topic or key to the characters the crew-memory key format
// allows. An empty name becomes "_", as in the KV key.
func safeName(s string) string {
	s = unsafeName.ReplaceAllString(strings.TrimSpace(s), "_")
	if s == "" {
		return "_"
	}
	return s
}

// uniqueName avoids the index file name and names that differ only in case,
// which collide on case-insensitive file systems.
func uniqueName(name string, taken map[string]bool) string {
	candidate := name
	if strings.EqualFold(candidate, strings.TrimSuffix(IndexFile, ".md")) {
		candidate = name + "_fact"
	}
	for n := 2; taken[strings.ToLower(candidate)]; n++ {
		candidate = fmt.Sprintf("%s-%d", name, n)
	}
	taken[strings.ToLower(candidate)] = true
	return candidate
}

func sortedTopics(byTopic map[string][]entry) []string {
	topics := make([]string, 0, len(byTopic))
	for t := range byTopic {
		topics = append(topics, t)
	}
	sort.Strings(topics)
	return topics
}

func factDocument(e entry) (string, error) {
	f := e.fact
	fm := frontMatter{
		Description: fmt.Sprintf("Crew fact %s / %s", e.topic, e.file),
		Metadata: factMeta{
			Namespace: f.Scope.Namespace,
			Crew:      f.Scope.Crew,
			Topic:     f.Topic,
			Key:       f.Key,
			LearnedBy: f.LearnedBy,
			LearnedAt: formatTime(f.LearnedAt),
			UsedAt:    formatTime(f.UsedAt),
			Source:    crewmemory.Bucket,
		},
	}
	return document(fm, strings.TrimSpace(cleanText(f.Value)))
}

func topicIndex(topic string, es []entry) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s\n\n", topic)
	for _, e := range es {
		fmt.Fprintf(&b, "- [%s](%s.md): %s\n", e.file, e.file, summary(e.fact.Value))
	}
	return document(indexMatter{Description: "Facts the crew learned about " + topic}, b.String())
}

func rootIndex(scope crewscope.Scope, topics []string, byTopic map[string][]entry) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "# Crew memory: %s/%s\n\n", scope.Namespace, scope.Crew)
	b.WriteString("Facts this crew learned about its domain and environment. Each topic directory\n")
	b.WriteString("has its own MEMORY.md that lists its facts; read a fact file when it is relevant.\n\n")
	if len(topics) == 0 {
		b.WriteString("No facts yet.\n")
	}
	for _, t := range topics {
		fmt.Fprintf(&b, "- [%s](%s/%s): %d fact(s)\n", t, t, IndexFile, len(byTopic[t]))
	}
	return document(indexMatter{Description: "Index of the memory kept by crew " + scope.Crew}, b.String())
}

// document renders YAML front matter followed by the body.
func document(front any, body string) (string, error) {
	y, err := yaml.Marshal(front)
	if err != nil {
		return "", fmt.Errorf("render front matter: %w", err)
	}
	return "---\n" + string(y) + "---\n\n" + strings.TrimRight(body, "\n") + "\n", nil
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.Format(time.RFC3339)
}

// summary is the first line of a value, shortened for an index line.
func summary(v string) string {
	line := strings.Map(flattenRune, strings.TrimSpace(cleanText(v)))
	line, _, _ = strings.Cut(line, "\n")
	line = strings.TrimSpace(line)
	r := []rune(line)
	if len(r) > summaryChars {
		return string(r[:summaryChars-3]) + "..."
	}
	return line
}

// sortedFacts returns a copy ordered by topic, key and time written, so name
// collisions are settled the same way whatever order the facts arrive in.
func sortedFacts(facts []crewmemory.Fact) []crewmemory.Fact {
	out := append([]crewmemory.Fact(nil), facts...)
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Topic != b.Topic {
			return a.Topic < b.Topic
		}
		if a.Key != b.Key {
			return a.Key < b.Key
		}
		return a.LearnedAt.Before(b.LearnedAt)
	})
	return out
}

// flattenRune turns line breaks and other control characters into spaces and
// drops the characters that make Markdown links, HTML and code, so an index
// line built from an untrusted value stays plain text.
func flattenRune(r rune) rune {
	switch {
	case r == '\n':
		return r
	case unicode.IsControl(r), r == ' ', r == ' ':
		return ' '
	case strings.ContainsRune("[]()<>`", r):
		return -1
	}
	return r
}

// cleanText drops NUL bytes, which no text file should carry.
func cleanText(s string) string {
	return strings.ReplaceAll(s, "\x00", "")
}

DEFINE DOMAIN kubemoot-docs
DESCRIPTION Authoring rules for Kubemoot end-user documentation (docs/ and the Hugo+Docsy site). Inherits ecosystem and kubemoot rules from the parent .claude/CLAUDE.md files.

# Punctuation

DEFINE COMPONENT no-em-dashes
DESCRIPTION Em dashes are a reflexive AI-writing tell - do not use them in docs prose

NEVER use an em dash (U+2014) in documentation prose, headings, or the site landing copy
ALWAYS recast the sentence instead: use a period for two independent clauses, a colon to introduce, or a comma/parentheses for an aside
ASSERT BAD: "Kubemoot runs a crew - small agents - on Kubernetes." → GOOD: "Kubemoot runs a crew of small agents on Kubernetes."
ASSERT BAD: "consensus by signal - agree, concern, block - over a bus" → GOOD: "consensus over a bus, where each specialist can agree, raise a concern, or object"
WHEN composing or editing a doc page THEN scan for "-" before saving and rewrite every occurrence
NEVER substitute an en dash (U+2013) or a double hyphen "--" as a stand-in - the goal is to recast, not re-punctuate
ASSERT numeric ranges (e.g. "10-15 tools") use a plain ASCII hyphen, never an en dash (U+2013)

# Voice

DEFINE COMPONENT concepts-over-jargon
DESCRIPTION End-user docs lead with concepts; internal tokens are reference detail, not landing copy

ASSERT the landing page and concept intros explain ideas in plain language, not code tokens
WHEN describing consensus on overview/landing pages THEN say what agents do (agree, raise a concern, object, abstain) rather than listing the literal signal identifiers
WHEN the literal signal names (`agree`, `concern`, `block`, `stand_aside`, `failure`) are needed THEN keep them in reference and concept-detail pages (e.g. Signals & Protocol), not in headline copy
ASSERT docs/ is end-user reference, not a diagnostic journal - strip thread IDs, dates in headings, and "verified empirically" framing

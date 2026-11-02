# Task: UTF-8 mention extraction and comment routing

Implement router.go in package mentionroute using only the Go standard library.
The host owns go.mod and acceptance_test.go: do not change them. The only deliverable
is router.go. Do not add files, subprocesses, network access or package init work.
Keep all state local to each call; concurrent calls must be safe.
Host constraints: imports are limited to bytes, errors, fmt, slices, sort,
strconv, strings, unicode and unicode/utf8. No init or TestMain functions,
compiler directives, dot/blank imports, or function calls in package variable
initializers are allowed. These narrow limits keep acceptance execution honest;
they are part of this task, not a general security sandbox.

Public API (the names and string values are exact):

    type State string
    const Pending State = "pending"
    const Busy State = "busy"
    const Finished State = "finished"
    type Author string
    const Human Author = "human"
    const Agent Author = "agent"
    type Delivery struct { Key string; When string }
    func Extract(text string, keys []string) []string
    func Route(state State, author Author, text string, keys []string) []Delivery

Requirements and acceptance criteria:

R1. UTF-8 and literal identity. Invalid UTF-8 text produces no mentions. A key is
valid iff it is nonempty valid UTF-8 and contains no Unicode whitespace, Unicode
control character, @, backslash or backtick. Ignore invalid keys. All other
characters, including dots, plus signs and emoji, are literal. Do not case-fold,
trim or Unicode-normalize keys or text. Do not modify the caller's key slice.

R2. Boundaries and ambiguity. A mention is @ immediately followed by a valid
known key. The rune before @, when present, must not be a Unicode letter, number
or mark, or one of underscore, hyphen, dot, plus, @. The rune after the full key,
when present, must not be a Unicode letter, number or mark, underscore or hyphen.
These boundary checks are rune-aware. At a given @, choose the longest matching
key satisfying both boundaries; move past the chosen mention. There is no regex
interpretation. A period or plus after a complete mention is punctuation unless
it participates in a longer known key. Do not match a known key as the prefix of
an unknown identifier such as @alice2, @alice-x or @alice followed by a letter.

R3. Ordering. Return each mentioned key once, in first-occurrence source order,
independent of key registration order. Duplicate registered keys do not create
duplicate mentions. No match returns an empty slice (nil is also valid).

R4. Inline code. On each nonquoted line outside a fence, any run of one or more
backticks opens a code span. Ignore its content through the next run of exactly
the same number of backticks on that line; other run lengths do not close it.
An unmatched opening run hides the remainder of that line only. Backticks do
not cross a newline. Mentions before and after a closed span remain eligible.
This is a defined Markdown subset; no other inline Markdown syntax is special.

R5. Fenced code. Outside a fence, a line beginning with zero to three ASCII
spaces then at least three backticks or at least three tildes opens a fence.
The rest of an opening line is an arbitrary info string. Ignore that entire
line and all fenced content. Close only with a line beginning with zero to
three ASCII spaces, the same fence character repeated at least as many times
as the opener, and then only ASCII spaces or tabs. Ignore the closing line.
A shorter run or a run with other trailing characters does not close a fence.
An unclosed fence hides the rest of the input. LF and CRLF are both newlines.

R6. Quotations, email and escapes. A line whose first non-space/tab character
is > is entirely ignored and never opens or closes a fence. Other rules are
applied only to the remaining lines. Email-shaped text is excluded by R2's
left boundary, including alice@example and alice+tag@example. An @ immediately
preceded by an odd-length consecutive run of backslashes is escaped; an even
run leaves it eligible. Backslash parity is checked before the ordinary left
boundary. Quotes written with ordinary quote marks have no special meaning.

R7. Human routing. Route calls Extract and produces one Delivery per extracted
key in the same order. For Human, Pending maps When to "start", Busy to "soon",
and Finished to "new". No mention means no delivery: there is no implicit target
or broadcast. A finished human comment explicitly starts a new execution.

R8. Agent authority and invalid values. Agent comments may route only in Busy,
with When "soon". Agent comments in Pending or Finished never start an execution.
Unknown State or Author values produce no deliveries, even with explicit
mentions. These checks are fail-closed and do not affect Extract. Route never
returns a delivery not justified by a literal eligible mention.

Deliver a small readable implementation. You may run the supplied tests as
development feedback, but only the host verifier's checks count as evidence.
The independent reviewer must explain any unmet requirement, not approve based
only on the engineer's assertions or a test command mentioned in model output.

package main

// These fixtures are host-authored inputs, not an implementation of the task.
// The harness freezes their exact bytes before asking any agent to work.
const fixtureModule = "module example.test/mentionroute\n\ngo 1.23\n"

const fixtureSpec = `# Task: UTF-8 mention extraction and comment routing

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
`

const fixtureStarter = `package mentionroute

type State string

const (
	Pending State = "pending"
	Busy State = "busy"
	Finished State = "finished"
)

type Author string

const (
	Human Author = "human"
	Agent Author = "agent"
)

type Delivery struct {
	Key string
	When string
}

// Extract returns the eligible literal mentions in source order.
func Extract(text string, keys []string) []string { return nil }

// Route applies the state and author permissions to eligible mentions.
func Route(state State, author Author, text string, keys []string) []Delivery { return nil }
`

func requirementIDs() []string {
	return []string{"R1", "R2", "R3", "R4", "R5", "R6", "R7", "R8"}
}

// The generated acceptance suite is deliberately independent of an extraction
// algorithm. It checks externally visible cases and permission invariants.
const fixtureAcceptance = `package mentionroute

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func checkKeys(t *testing.T, text string, keys, want []string) {
	t.Helper()
	before := append([]string(nil), keys...)
	got := Extract(text, keys)
	if len(got) != len(want) || len(want) != 0 && !reflect.DeepEqual(got, want) {
		t.Fatalf("Extract(%q, %q) = %q; want %q", text, keys, got, want)
	}
	if len(keys) != len(before) || len(keys) != 0 && !reflect.DeepEqual(keys, before) {
		t.Fatalf("Extract changed input keys: before %q, after %q", before, keys)
	}
}

func TestAcceptanceR1LiteralUTF8(t *testing.T) {
	keys := []string{"Dev", "dev", "dev.ops+", "工程師", "🧪", "café", "cafe\u0301", "", "bad key", "a@b", "bad\\key", string([]byte{255})}
	checkKeys(t, "@工程師 @Dev @dev.ops+ @🧪 @cafe\u0301 @café @dev @DEV", keys,
		[]string{"工程師", "Dev", "dev.ops+", "🧪", "cafe\u0301", "café", "dev"})
	checkKeys(t, "@bad key @a@b @bad\\key", keys, nil)
	checkKeys(t, "@dev "+string([]byte{255})+" @Dev", keys, nil)
	checkKeys(t, "@dev", nil, nil)
}

func TestAcceptanceR2BoundariesAndLongest(t *testing.T) {
	keys := []string{"dev", "dev.ops", "dev.ops+", "工程", "工程師", "é", "alice"}
	checkKeys(t, "(@dev), @dev.ops+! @工程師 @é?", keys, []string{"dev", "dev.ops+", "工程師", "é"})
	checkKeys(t, "x@alice 7@alice _@alice -@alice .@alice +@alice @@alice 字@alice \u0301@alice", keys, nil)
	checkKeys(t, "@alice2 @alice-x @alice_ @alice字 @alice\u0301 @工程師字", keys, nil)
	checkKeys(t, "@alice. @é+", keys, []string{"alice", "é"})
	checkKeys(t, "@devX @dev-ops @dev_ops @dev.ops+", keys, []string{"dev.ops+"})
}

func TestAcceptanceR3DeduplicationOrder(t *testing.T) {
	checkKeys(t, "@c @b @c @a @b", []string{"a", "b", "c", "b"}, []string{"c", "b", "a"})
	checkKeys(t, "no explicit target", []string{"a", "b"}, nil)
	checkKeys(t, "", []string{"a"}, nil)
}

func TestAcceptanceR4InlineCode(t *testing.T) {
	bt := "\x60"
	keys := []string{"a", "b", "c"}
	checkKeys(t, "@a "+bt+"@b"+bt+" @c", keys, []string{"a", "c"})
	checkKeys(t, bt+bt+"@b "+bt+"@c"+bt+" "+bt+bt+" @a", keys, []string{"a"})
	checkKeys(t, "@a "+bt+"@b\n@c", keys, []string{"a", "c"})
	checkKeys(t, bt+"@a"+bt+bt+" @b"+bt+" @c", keys, []string{"c"})
}

func TestAcceptanceR5FencedCode(t *testing.T) {
	bt := "\x60"
	keys := []string{"a", "b", "c"}
	text := strings.Join([]string{
		"@a", bt+bt+bt+"go @b", "@b", "  "+bt+bt+bt+"  ", "@c",
	}, "\n")
	checkKeys(t, text, keys, []string{"a", "c"})
	checkKeys(t, "~~~txt\r\n@b\r\n   ~~~~\t\r\n@a", keys, []string{"a"})
	text = strings.Join([]string{
		bt+bt+bt+bt, "@b", bt+bt+bt, "@c", bt+bt+bt+bt+" trailing", "@a", bt+bt+bt+bt+bt, "@c",
	}, "\n")
	checkKeys(t, text, keys, []string{"c"})
	checkKeys(t, "@a\n~~~\n@b\n"+bt+bt+bt+"\n@c", keys, []string{"a"})
}

func TestAcceptanceR6QuotesEmailEscapes(t *testing.T) {
	keys := []string{"a", "b", "example", "dev"}
	checkKeys(t, "> @a\n \t> @b\n@dev\n'@a'", keys, []string{"dev", "a"})
	checkKeys(t, "alice@example alice+tag@example x.y@example @dev", keys, []string{"dev"})
	checkKeys(t, "\\@a \\\\@b \\\\\\@dev", keys, []string{"b"})
	bt := "\x60"
	checkKeys(t, "> "+bt+bt+bt+"\n@a", keys, []string{"a"})
	checkKeys(t, bt+bt+bt+"\n> "+bt+bt+bt+"\n@b\n"+bt+bt+bt+"\n@a", keys, []string{"a"})
}

func checkDeliveries(t *testing.T, state State, author Author, text string, keys []string, want []Delivery) {
	t.Helper()
	got := Route(state, author, text, keys)
	if len(got) != len(want) || len(want) != 0 && !reflect.DeepEqual(got, want) {
		t.Fatalf("Route(%q,%q,%q) = %+v; want %+v", state, author, text, got, want)
	}
}

func TestAcceptanceR7HumanRouting(t *testing.T) {
	keys := []string{"a", "b"}
	for _, tt := range []struct { state State; when string }{
		{Pending, "start"}, {Busy, "soon"}, {Finished, "new"},
	} {
		t.Run(string(tt.state), func(t *testing.T) {
			checkDeliveries(t, tt.state, Human, "@b @a @b", keys,
				[]Delivery{{Key: "b", When: tt.when}, {Key: "a", When: tt.when}})
			checkDeliveries(t, tt.state, Human, "plain comment", keys, nil)
			checkDeliveries(t, tt.state, Human, "alice@a", keys, nil)
		})
	}
}

func TestAcceptanceR8AgentAuthority(t *testing.T) {
	keys := []string{"a", "b"}
	checkDeliveries(t, Busy, Agent, "@b @a", keys, []Delivery{{Key: "b", When: "soon"}, {Key: "a", When: "soon"}})
	for _, tt := range []struct { state State; author Author }{
		{Pending, Agent}, {Finished, Agent}, {State("unknown"), Human},
		{State(""), Human}, {Busy, Author("unknown")}, {Busy, Author("")},
		{State("unknown"), Agent},
	} {
		checkDeliveries(t, tt.state, tt.author, "@a", keys, nil)
	}
	checkKeys(t, "@a", keys, []string{"a"})
}

func TestAcceptanceConcurrentIndependentCalls(t *testing.T) {
	var workers sync.WaitGroup
	for i := 0; i < 24; i++ {
		workers.Add(1)
		go func(i int) {
			defer workers.Done()
			key := fmt.Sprintf("worker%d", i)
			for n := 0; n < 20; n++ {
				got := Route(Busy, Agent, "@"+key+" @other", []string{key})
				if len(got) != 1 || got[0] != (Delivery{Key: key, When: "soon"}) {
					t.Errorf("independent call %d produced %+v", i, got)
					return
				}
			}
		}(i)
	}
	workers.Wait()
}
`

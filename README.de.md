# agent-adaptor

[English](./README.md) | [简体中文](./README.zh-CN.md) | [日本語](./README.ja.md) | [한국어](./README.ko.md)

`agent-adaptor` v2 ist ein SDK mit einer kleinen, verständlichen API, die verschiedene Agents – `Codex`, `Claude Code`, `Cursor` und `CodeBuddy` – über dieselbe Schnittstelle ansteuert und zusätzliche Fähigkeiten über den einfachen Aufruf hinaus bietet.

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.4"}))
result, err := agent.Send(ctx, "Fix the failing tests").Result()
```

Für den Wechsel zu Claude Code genügt es, den Driver im Konstruktor auszutauschen. Der übrige Code bleibt unverändert.

## Fähigkeiten im Überblick

- **Einheitliche Konfiguration**: Eine API steuert Skills, MCP, System-Prompts, Modelle, Sandbox, Tools und Genehmigungen über alle Agents hinweg.
- **Streaming-Antworten**: Der optionale Event-Stream unterscheidet Denkprozess, Textausgabe, Tool-Aufrufe und Entscheidungsanfragen.
- **Konversationsverwaltung**: Konversationen lassen sich fortsetzen und verzweigen. Verwenden Sie Ihre eigene fachliche ID, etwa eine Ticketnummer oder Benutzer-ID, als Konversationsschlüssel, ohne die zugrunde liegenden Sitzungen selbst verwalten zu müssen.
- **Menschliche Entscheidungen**: Beantworten Sie Fragen, prüfen Sie gefährliche Befehle und bestätigen Sie Pläne über Callbacks oder Events. Der Host kann den aktiven Responder aufbewahren und aus einer anderen HTTP-Anfrage beantworten. Die Speicherung von Genehmigungsprotokollen liegt beim Host.

## Weiterführende Funktionen

- **Strukturierte Ausgabe**: Definieren Sie eine Go-Struktur und rufen Sie `RunAs[T]` auf. Der Agent führt die Aufgabe mit einer Ausgabevorgabe aus und liefert ein ausgefülltes Objekt zurück.
- **Mehrere Protokolle**: Die eingebauten A2A-/AGUI-Brücken stellen einen Agent mit einer Zeile als standardkonformen Agent mit SSE- und AGUI-Streaming bereit. Für einen vollständigen Dienst brauchen Sie dann nur noch ein eigenes Frontend oder einen Client; ein ausführbares assistant-ui-Frontend ist enthalten.
- **Teams**: Ein vom Host gewählter Leader koordiniert Mitglieder mit beliebigen Drivers, etwa Codex als Leader mit einem Planungsmitglied auf Codex, einem Implementierungsmitglied auf Claude und einem Reviewer auf Cursor, sowie entfernte A2A-Mitglieder. Aufgabenmeldungen und Genehmigungsanfragen der Worker erscheinen gemeinsam im Event-Stream des Leaders. Siehe [Team-Leitfaden](./docs/team.md) und das Showcase examples/showcases/team-agent-workflow.
- **Agent-Isolation**: Klonen Sie die Agent-Konfiguration des Rechners in ein eigenes Verzeichnis und verknüpfen Sie den Anmeldestatus. Konfigurationsänderungen bleiben getrennt, sodass mehrere Codex-/Claude-Code-Instanzen parallel an unterschiedlichen Aufgaben oder in verschiedenen Rollen arbeiten können.

## Installation

```bash
go get github.com/agent-dance/agent-adaptor/v2
```

Erfordert Go 1.26.5 oder neuer.

Wichtig: **Die entsprechende Agent-CLI muss zur Laufzeit bereits installiert und angemeldet sein.**

## Schnellstart

```go
package main

import (
	"context"
	"fmt"
	"log"

	adaptor "github.com/agent-dance/agent-adaptor/v2"
	"github.com/agent-dance/agent-adaptor/v2/codex"
)

func main() {
	agent := adaptor.New(
		codex.Driver(codex.Config{Model: "gpt-5.4"}),
		adaptor.WithWorkspace("/path/to/repository"),
	)

	result, err := agent.Send(context.Background(), "Fix the failing tests").Result()
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}
```

Die vier eingebauten Drivers werden auf dieselbe Weise konstruiert, jeweils mit ihrer eigenen `Config`:

```go
codexAgent := adaptor.New(codex.Driver(codex.Config{}))
claudeAgent := adaptor.New(claude.Driver(claude.Config{}))
cursorAgent := adaptor.New(cursor.Driver(cursor.Config{}))
codeBuddyAgent := adaptor.New(codebuddy.Driver(codebuddy.Config{}))
```

## Streaming-Ausführung

`Send` startet eine Ausführung und gibt einen `Stream` zurück. Lesen Sie `Events()` für den laufenden Fortschritt oder warten Sie synchron mit `Result()`:

```go
stream := agent.Send(ctx, "Explain the patch that is about to be committed")
defer stream.Cancel()

for event := range stream.Events() {
	switch event := event.(type) {
	case adaptor.TextDelta:
		fmt.Print(event.Text)
	case adaptor.Thinking:
		fmt.Fprint(os.Stderr, event.Text)
	case adaptor.ToolCall:
		if event.Phase == adaptor.PhaseStart {
			fmt.Printf("\n[tool call: %s]\n", event.Name)
		}
	case *adaptor.ApprovalRequest:
		_ = event.Approve(ctx)
	case adaptor.Dropped:
		log.Printf("backpressure dropped %d incremental events", event.Count)
	}
}

result, err := stream.Result()
```

Text, Denkprozess, Tool-Aufrufe und ihre Ergebnisse, Prozessinformationen, Lebenszyklus, Nachrichtenergebnisse und Genehmigungsanfragen laufen über diesen einen Stream. Jede Provider-Runde erzeugt `TurnEnd`. Das letzte Event ist `Done{Result, Err}`. Es wird nach Validierung, Checkpoint-Verarbeitung und Ressourcenfreigabe veröffentlicht und enthält denselben Ergebniszeiger und Fehler wie `stream.Result()`. `RunFinished` dient nur der Information.

Rufen Sie `Cancel()` auf, wenn Sie das Lesen vorzeitig beenden. Der Aufruf ist idempotent.

## Nachrichten während einer Ausführung

Senden Sie zusätzliche Hinweise an den zurückgegebenen Stream und verwenden Sie eine stabile Host-ID zur Deduplizierung:

```go
stream := agent.Send(ctx, "Fix the failing tests")
err := stream.Send(ctx, "Keep the public API unchanged",
	adaptor.MessageID("review-comment-42"),
	adaptor.From(adaptor.Author{Kind: adaptor.AuthorHuman, ID: "reviewer"}),
	adaptor.When(adaptor.Soon),
)
```

`Soon` ist der Standard: Die Nachricht wird an der frühesten unterstützten Grenze zugestellt oder am Rundenende in einer Fortsetzung verarbeitet. `AfterTurn` wartet auf eine Fortsetzungsrunde. `Interrupt` bricht die aktive Runde ab und setzt innerhalb derselben Ausführung fort; Drivers ohne diese Unterstützung lehnen mit `ErrMessageUnsupported` ab. Siehe [Driver-Matrix](./docs/streaming.md#12-message-delivery-capabilities).

Ein nil-Fehler beim Senden bedeutet, dass die Nachricht angenommen wurde. `MessageUpdate` meldet Annahme, Zustellung oder Fehlschlag; bei einem Fehlschlag bleibt der ursprüngliche Text erhalten. `Result.Messages()` enthält die endgültigen Ergebnisse, `Undelivered()` die fehlgeschlagenen Nachrichten. Annahmefehler sind `*MessageError` mit ID, Text, Grund und umhüllter Ursache; eine beendete Ausführung lässt sich mit `ErrRunFinished` erkennen. Der Kontext eines Stream-Sendeaufrufs kann eine noch nicht vom Driver übernommene Nachricht zurückziehen. Nach der Übernahme entscheidet die Zustellungsbestätigung über ihr Ergebnis.

`MessageID` dedupliziert innerhalb des Threads und bewahrt bis zu 1000 zugestellte IDs für sieben Tage auf. Für die Wiederherstellung ist ein persistenter Thread-Store erforderlich. Ein Absturz nach der Annahme durch den Provider, aber vor dem Speichern der Bestätigung, kann eine erneute Zustellung auslösen; die Deduplizierung schließt dieses Absturzfenster nicht.

## Standby

Verwenden Sie den Aufgabenmodus (`Thread.Send`), wenn jede Anfrage ein abgeschlossenes Ausführungsergebnis benötigt. Standby eignet sich für Chats oder Messenger-Bots, die fortlaufend Nachrichten empfangen und beantworten:

```go
func openChat(serverCtx context.Context, agent *adaptor.Agent) (adaptor.Standby, error) {
	sb, err := agent.Thread("chat-42").Standby(serverCtx)
	if err != nil {
		return nil, err
	}
	events := sb.Events() // subscribe before accepting input
	go func() {
		for event := range events {
			switch e := event.(type) {
			case adaptor.TurnEnd:
				if e.Err == nil && e.Result != nil {
					fmt.Println(e.Result.Text)
				}
			case adaptor.Done:
				if e.Err != nil {
					log.Print(e.Err)
				}
			}
		}
	}()
	return sb, nil
}
```

Öffnen Sie Standby mit einem Kontext, der für die Lebensdauer des Servers gilt. Ein HTTP-Handler kann anschließend `sb.Send(r.Context(), text, adaptor.MessageID(id))` aufrufen: Dieser Kontext begrenzt nur die Annahme; angenommene Arbeit gehört zu `serverCtx`. Eingaben ergänzen eine laufende Ausführung oder starten eine neue, wenn Standby untätig ist. Ausführungsoptionen wie `WithModel` stellen eine separate Ausführung hinter die aktuelle Arbeit.

Standby hat `Send`, `Events` und `Cancel`, aber kein übergreifendes `Result()`. Jede Provider-Runde erzeugt `TurnEnd`, jede Ausführung `Done`. `sb.Cancel()` stoppt die aktuelle Verarbeitung und meldet wartende Nachrichten mit `MessageCancelled` als fehlgeschlagen; danach nimmt Standby weiter Nachrichten an. Brechen Sie `serverCtx` ab, um Standby zu beenden und den Event-Kanal zu schließen. Vor dem ersten Aufruf von `Events()` werden seine Events verworfen.

Ein aktives Standby hält die Schreibberechtigung für den Thread auch im Leerlauf; Sendeaufrufe im Aufgabenmodus liefern `ErrThreadBusy`. Ein erneutes Öffnen desselben Schlüssels auf demselben Agent gibt den vorhandenen aktiven Handle zurück, dessen Lebensdauer weiterhin an den ersten Kontext gebunden ist.

Fehlgeschlagene Ausführungen beenden Standby nicht. Nur klassifizierte Rate-Limit-, Netzwerk- oder Provider-Serverfehler werden automatisch wiederholt, höchstens einmal. Nicht zugestellte `run_failed`-Nachrichten werden mit denselben IDs in die nächste Ausführung übernommen: Beobachter können zunächst einen Fehlschlag und später eine Zustellung sehen. Ohne Wiederholung warten übernommene Eingaben auf die nächste Nachricht des Hosts. Die Grenzen von Zustellung und Wiederherstellung beschreibt der [Streaming-Leitfaden](./docs/streaming.md).

## Menschliche Genehmigung und Sandbox

Sandbox-Stufe, Such- und Browsersteuerung, Genehmigungsmodi und Nachrichtengrenzen stehen in derselben `Policy`. Der Konstruktor legt Standardwerte fest; `Send` kann die gesamte Policy pro Ausführung ersetzen:

```go
reviewer := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Sandbox:   adaptor.ReadOnly,
		WebSearch: adaptor.FeatureDeny,
		Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAutoApprove,
		},
	}),
)
```

Die Sandbox kennt drei Stufen: `ReadOnly`, `WorkspaceWrite` und `Unrestricted`. Vorlagen wie `PolicyReadOnly` setzen lediglich `Sandbox`. Unterstützt der gewählte Driver eine Dimension nicht, folgt vor dem Prozessstart ein ausdrücklicher Fehler statt einer stillen Herabstufung.

Für Genehmigungen gibt es zwei Verarbeitungsformen. Wählen Sie eine davon. Ein Callback eignet sich für CLIs und unbeaufsichtigte Ausführungen:

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Approvals: adaptor.ApprovalPolicy{Question: adaptor.QuestionAsk},
	}),
	adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		switch req.Kind {
		case adaptor.ApprovalPermission:
			return req.Approve(ctx)
		case adaptor.ApprovalQuestion:
			return req.Answer(ctx, "Use PostgreSQL")
		default:
			return req.Deny(ctx, "the plan needs human confirmation")
		}
	}),
)
```

Für Callback-basierte Policies stehen `adaptor.ApproveAll()` und `adaptor.DenyAll(reason)` bereit. Sie beantworten bereits an den Callback geleitete Anfragen; sie aktivieren keine nicht unterstützten Genehmigungsmodi. Claude und CodeBuddy unterstützen interaktive Anfragen. Codex unterstützt Berechtigungsgenehmigungen über app-server, auch für MCP-Tools; Planprüfungen und Fragen mit Eingabe unterstützt es nicht. Cursor weist `Ask` nicht als unterstützte Fähigkeit aus.

Ohne Callback werden Anfragen über Events verarbeitet: Im Event-Stream kommt ein `*adaptor.ApprovalRequest` mit eigenem Responder an. Der Host kann ihn aufbewahren und später aus einer beliebigen Goroutine oder einer anderen HTTP-Anfrage beantworten, wie es Web-Anwendungen benötigen:

```go
func consumeApprovals(ctx context.Context, stream adaptor.Stream,
	publish func(*adaptor.ApprovalRequest)) (*adaptor.Result, error) {
	for event := range stream.Events() {
		if req, ok := event.(*adaptor.ApprovalRequest); ok {
			publish(req) // the host retains the live responder for its UI
		}
	}
	return stream.Result()
}
```

Der Host kann später an einem ergänzenden Endpunkt `req.Approve(r.Context())`, `req.Deny(r.Context(), reason)` oder `req.Answer(r.Context(), text)` aufrufen. `NoticeApprovalResolved` enthält `Data["request_id"]`, damit die UI die wartende Anfrage entfernen kann. Das Speichern eines Audit-Eintrags serialisiert den aktiven Responder nicht.

Eine Anfrage kann genau einmal beantwortet werden. Doppelte Antworten, ein unpassender Kind-Wert und beendete Ausführungen liefern stabile Fehler (`ErrApprovalResolved`, `ErrApprovalKindMismatch`, `ErrApprovalExpired`); auch eine Anfrage mit Nullwert blockiert niemals unbegrenzt. Wurde beim Eintreffen weder `Events()` aufgerufen noch ein `OnApproval`-Handler eingerichtet, greift die Timeout-Ersatzentscheidung sofort und erzeugt einen Lebenszyklus-Hinweis. Andernfalls übernimmt `OnTimeout` aus `Policy.Approvals`, wenn bis zur Frist niemand antwortet; nach einer Ablehnung gilt `OnReject`. Wo der Host wartende Anfragen aufbewahrt, entscheidet er selbst; das Audit ist nicht auf Prozessspeicher beschränkt.

Ein vollständiges, ausführbares Web-Beispiel für HITL bietet [`web-chat/assistant-ui`](./examples/web-chat/assistant-ui): Entscheidungen erscheinen als `dec.*`-Werkzeugaufrufe mit Approve/Deny, und die hosteigenen Endpunkte `/decision/pending` und `/decision/resolve` beantworten die laufende Anfrage genau einmal.

## Mehrere Konversationsrunden

Direkte `Agent.Send`-Aufrufe sind unabhängig voneinander, als würde jedes Mal ein neuer anonymer Thread verwendet. Benannte Threads nutzen standardmäßig einen Agent-lokalen Store im Speicher. Mit `WithThreadStore` lässt sich ein gemeinsam genutzter oder persistenter Store einbinden:

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithThreadStore(memory.NewStore()),
)
defer agent.Close(context.Background())

thread := agent.Thread("tenant-42/issue-123")        // continue the mapped conversation if it exists, otherwise create it
result, err := thread.Send(ctx, "Keep investigating this problem").Result()

only := agent.Thread("tenant-42/issue-123", adaptor.ResumeOnly()) // resume only, never create
branch := thread.Fork("tenant-42/issue-123/plan-b")               // fork from the current progress
```

Dabei gelten folgende Regeln:

- **Der Konversationsschlüssel ist die Zeichenfolge des Hosts.** Das SDK speichert und vergleicht sie unverändert. Eine vollständig neue Konversation benötigt einen neuen Schlüssel; das SDK bietet keinen Einstieg, um einen alten Schlüssel an eine neue Konversation zu binden.
- **Ein Thread hat einen Schreiber**, abgesichert durch eine Lease. Während einer anderen Ausführung oder eines aktiven Standby schlägt `thread.Send` im Aufgabenmodus über seinen Stream mit `ErrThreadBusy` fehl. Weitere Eingaben gehören an den vorhandenen Stream oder Standby.
- **Vor dem Fortsetzen wird die Kompatibilität geprüft.** Driver, Modell, aufgelöster Workspace, Konfiguration, Skills und MCP gehen in den Fingerprint ein. Änderungen verhindern eine falsche Wiederverwendung der Konversation.
- **Fehler beschädigen den Zustand nicht.** Exits ungleich null, Protokollfehler und Abbrüche erzeugen keinen gültigen Checkpoint. Der zuvor gesunde Konversationsdatensatz bleibt unverändert.
- **Residente Prozesse werden standardmäßig weiterverwendet.** Unter Windows, macOS und Linux verwenden Claude, CodeBuddy und Codex für die Runden eines expliziten Threads denselben Prozess. Mit `adaptor.WithSpawn()` erzwingen Sie für eine oder jede Runde einen neuen Prozess. Cursor und zustandslose Aufrufe starten pro Runde einen neuen Prozess. Neue Sendeaufrufe nach Beginn von `Agent.Close` liefern `ErrAgentClosed`.

Für Szenarien in einem Prozess genügt `memory.NewStore()`; für Persistenz implementieren Sie `threadstore.Store`.

## Strukturierte Ausgabe

```go
type ReleasePlan struct {
	Filename  string `json:"filename"`
	MediaType string `json:"media_type"`
	Summary   string `json:"summary"`
	Content   string `json:"content"`
}

plan, result, err := adaptor.RunAs[ReleasePlan](ctx, agent,
	"Produce the release plan as a Markdown file artifact.")
if err != nil {
	return err
}
fmt.Printf("%s (%s)\n%s\n", plan.Filename, result.RunID, plan.Content)
```

Das Schema wird aus dem Go-Typ abgeleitet und nutzt vorrangig die native Schema-Prüfung des Providers. Unterstützt der aktuelle Transport oder die Policy diese nicht, fällt die Ausführung automatisch auf Prompt-Vorgaben und lokale Validierung zurück. Nur wenn beides nicht verfügbar ist, schlägt sie vor dem Start fehl. Die Rückgabewerte enthalten sowohl den typisierten Wert als auch das vollständige `Result` für die Auditierung.

Details finden Sie im [`structured-output`-Beispiel](./examples/structured-output) und in der [Dokumentation zur strukturierten Ausgabe](./docs/structured-output.md).

## Optionen und Ressourcen

Optionen nutzen ein gemeinsames Vokabular; der Typ trennt ihren Gültigkeitsbereich zur Kompilierzeit:

| Typ | Gültigkeitsbereich |
|---|---|
| `Option` | ausschließlich `adaptor.New` |
| `CallOption` | `Agent.Send`, `Thread.Send` oder `Standby.Send` |
| `SharedOption` | Standardwerte für Konstruktion und Ausführung; der Aufruf überschreibt den Konstruktor |
| `MessageOption` | `From`, `MessageID`, `When`; auch als `CallOption` zulässig und die einzigen Optionen für `Stream.Send` |

Es gilt eine Zusammenführungsregel: Der nähere Wert gewinnt, Skills werden angehängt, und alles andere wird gemäß seinem eigenen Vertrag ersetzt oder zusammengeführt.

Dieselben Optionen decken die wichtigsten Einstellungen jedes Agents ab:

| Was Sie steuern möchten | Option |
|---|---|
| Modell | `WithModel` |
| System-Prompt | `WithInstructions` |
| Arbeitsverzeichnis | `WithWorkspace`, für isolierte Arbeitsbäume `WithWorkspaceSpec` |
| Skills | `WithSkills` mit `skill.Dir` / `skill.FS` / `skill.Inline` / `skill.Key` / `skill.Require` |
| MCP | `WithMCP` mit `mcp.Stdio` / `mcp.HTTP` / `mcp.SSE` |
| Sandbox, Suche, Browser-Tools, Genehmigungen, Nachrichtengrenzen | `WithPolicy`, interaktiv zusätzlich `OnApproval` |
| Konfigurationsverzeichnis und Ressourcen | `WithProfile`, `WithProfileResources` |
| Timeout, Audit-Metadaten, Identität des Aufrufers | `WithTimeout`, `WithMetadata`, `WithIdentity` |
| Konversationspersistenz | `WithThreadStore` |

```go
agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithModel("gpt-5.4"),
	adaptor.WithInstructions("You are this repository's reviewer: read the code only, state the conclusion before the evidence."),
	adaptor.WithSkills(skill.Dir("./skills/review")),
	adaptor.WithMCP(mcp.Stdio("repo-tools", "repo-mcp", mcp.Args("serve"))),
	adaptor.WithProfile(profile.Dedicated("./profiles/reviewer")),
	adaptor.WithTimeout(10*time.Minute),
)

result, err := agent.Send(ctx, "Review this change",
	adaptor.WithModel("gpt-5.4-mini"),
	adaptor.WithSkills(skill.Require(skill.Dir("./skills/security"), "this run must pass the security review")), // appends, does not displace the default skills
	adaptor.WithMetadata("request_id", "review-42"),
).Result()
```

Dieselbe Konfiguration mit einem anderen Driver ergibt einen anderen Agent. Unterstützt ein Driver eine Fähigkeit nicht, folgt vor dem Start ein ausdrücklicher Fehler statt eines stillen Weglassens.

```go
codexReviewer := adaptor.New(codex.Driver(codex.Config{}), reviewerOptions...)
claudeReviewer := adaptor.New(claude.Driver(claude.Config{}), reviewerOptions...)
```

## Vom Host definierte Tools

Erweitern Sie einen Agent direkt mit typisierten Go-Funktionen, ohne selbst einen MCP-Server aufzubauen oder zu betreiben:

```go
type SearchInput struct {
	Query string `json:"query" jsonschema:"required"`
}

type SearchOutput struct {
	Files []string `json:"files"`
}

searchRepo := tool.Define(
	"search_repo",
	"Match files using a repository-relative glob.",
	func(ctx context.Context, in SearchInput) (SearchOutput, error) {
		files, err := filepath.Glob(in.Query)
		return SearchOutput{Files: files}, err
	},
	tool.ReadOnly(),
	tool.Idempotent(),
	tool.Revision("search_repo/v1"),
)

agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithTools(searchRepo),
)
defer agent.Close(context.Background())
```

`WithTools` gilt nur bei der Konstruktion und ersetzt den gesamten Tool-Satz. Schemas werden standardmäßig aus den Go-Typen des Handlers abgeleitet; alternativ kann ein ausdrückliches Standard-JSON-Schema angegeben werden. `tool.Reject(code, message)` meldet einen fachlichen Fehler, der dem Modell gefahrlos gezeigt werden kann. Nur vom Paket erzeugte Ablehnungen passieren diese Grenze; gewöhnliche Fehler, nachgebildete Ablehnungen und Panics werden bereinigt. Jedes von einem zustandsbehafteten Thread verwendete Tool benötigt `tool.Revision`, damit Änderungen am Verhalten des Handlers in die Kompatibilitätsprüfung beim Fortsetzen eingehen.

MCP ist hier nur der interne Zustellmechanismus. Bestehende oder entfernte MCP-Server werden weiterhin über `WithMCP` eingebunden. Die eingebauten Drivers materialisieren Tools in einem isolierten, vom SDK verwalteten Profil und lassen das konfigurierte native Profil unberührt. Jeder Agent erhält einen nicht vorhersehbaren Namen für die Umgebungsvariable seines Bearer-Tokens; ein anderer MCP-Server kann diesen Zugangsdatenkanal nicht unter einem Alias übernehmen. Lebenszyklus, Schema, Fehler, Sicherheit und Thread-Semantik beschreibt der [Vertrag für Host-definierte Tools](./docs/tools.md).

## Agent-Isolation

`WithProfile` bestimmt das Konfigurationsverzeichnis des Providers für einen Agent. `profile.CloneNative` klont ein eigenständiges Profil aus der nativen Konfiguration des Rechners und übernimmt auf Wunsch Einstellungen, MCP und Skills. Der Anmeldestatus wird über einen Link geteilt, statt Tokens zu kopieren:

```go
worker := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithProfile(profile.CloneNative("/var/agents/worker-1",
		profile.CopySettings(),
		profile.CopyMCP(),
		profile.CopySkills(),
		profile.LinkAuth(), // share the machine's login state via a symlink, so local re-logins are picked up automatically
	)),
)
```

So kann dieselbe CLI mehrere Instanzen parallel betreiben, je Rolle oder Aufgabe. Konfigurationsänderungen bleiben voneinander und von den lokal verwendeten `~/.claude` und `~/.codex` getrennt:

```go
isolated := func(dir string) adaptor.Option {
	return adaptor.WithProfile(profile.CloneNative(dir,
		profile.CopySettings(), profile.LinkAuth()))
}

planner := adaptor.New(codex.Driver(codex.Config{}),
	isolated("/var/agents/planner"),
	adaptor.WithPolicy(adaptor.PolicyReadOnly),
)
implementer := adaptor.New(claude.Driver(claude.Config{}),
	isolated("/var/agents/implementer"),
	adaptor.WithWorkspace("/repo/worktrees/feature-x"),
)
```

Es gibt drei weitere Möglichkeiten: `profile.Native()` verwendet die native Konfiguration des Rechners direkt, `profile.Dedicated(dir)` legt ein selbst verwaltetes Verzeichnis fest, und `profile.CloneFrom(src, dst, ...)` leitet ein Profil aus einem Vorlagenverzeichnis ab. Das Profil geht in den Fingerprint der Konversation ein. Deshalb ist es nur eine Konstruktionsoption und kann nicht pro Aufruf gewechselt werden.

Mit `agent.ProfileState(ctx)` sehen Sie, welche deklarierten Ressourcen tatsächlich materialisiert wurden und ob der Driver sie akzeptiert. `agent.SyncProfile(ctx)` materialisiert sie ausdrücklich. Beide unterscheiden gewünschte Ressourcen von beobachteter Materialisierung. Eine vollständige Anleitung bietet das [`profiles`-Beispiel](./examples/profiles).

## Ergebnisse und Fehler

Erfolg gibt `*Result, nil` zurück. Fehlschläge werden ausschließlich über Gos `error` gemeldet: Eine fachlich fehlgeschlagene Ausführung liefert einen `*RunError` mit dem verfügbaren `Result`, während Infrastrukturfehler gewöhnliche umhüllbare Fehler sind.

```go
result, err := agent.Send(ctx, prompt).Result()
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		log.Printf("run failed: %s; available summary: %s", runErr.Reason, runErr.Result.Summary)
	}
	return err
}
```

Die Ausgabeschichten von `Result` bleiben voneinander getrennt:

| Feld | Inhalt |
|---|---|
| `Text` | Der nutzerseitige Antworttext der letzten Provider-Runde |
| `Summary` | Eine kurze Zusammenfassung für Listen, Logs und Issue-Kommentare |
| `Raw()` | Vollständige stdout und stderr sowie die letzte erkannte offizielle Abschlusspayload |
| `Transcript()` | Vom Provider geparste Einträge aller Runden sowie vom Core aufgezeichnete zusätzliche Zustellungen |
| `Services()` | In dieser Ausführung tatsächlich beobachtete Runtime-Services |
| `Decode()` | Validierte strukturierte Ausgabe |
| `Turns` / `Usage` | Anzahl der Provider-Runden und Summe des beobachteten Verbrauchs |
| `Messages()` / `Undelivered()` | Endgültige Nachrichtenergebnisse in Annahmereihenfolge / fehlgeschlagene Nachrichten mit Originaltext |
| `Model` / `Provider` / `Metadata` | Provider- und Audit-Informationen |

`Text` enthält kein rohes stdout und hängt weder Zusammenfassung noch Abschlusspayload des Providers automatisch an. `Done` und `Stream.Result()` liefern dasselbe Ergebnis. Bei fachlichen Fehlern steht das verfügbare Ergebnis in `RunError.Result`; der direkt zurückgegebene Ergebniswert ist nil.

## Anbindung an Anwendungen

[AG-UI-1.0-Protokollvertrag](./docs/agui.md)

**Web-Frontends**: Eine Zeile verpackt einen Agent in einen `http.Handler` für das AG-UI-Protokoll. AG-UI-kompatible Clients wie assistant-ui mit `@ag-ui/client` können sich direkt verbinden:

```go
mux.Handle("/agent", sse.Handler(agent, sse.Options{
	Protocol: sse.AGUI,
}))
```

Für Chat-Oberflächen startet `agui.RunRegistry` im Leerlauf eine Thread-Ausführung und leitet weitere Eingaben an deren aktiven Stream weiter. Dabei gilt der unveränderte Kontextvertrag von Stream.Send: Ein Abbruch kann noch nicht abgeholte Eingaben zurückziehen. Für aufgezeichnete Läufe stellt `aguisession.Session.MessageHandler` `POST /message` bereit: Die Zustellung wird vom Kontext der ergänzenden Anfrage entkoppelt, angenommene Eingaben werden vor dem Abschluss als user-Nachricht im Verlauf des Laufs aufgezeichnet, und Zustellungsbestätigungen erscheinen im ursprünglichen SSE-Stream. Das assistant-ui-Beispiel nutzt das für Hinweise während des Laufs. Der optionale `sessionrecorder` zeichnet standardmäßig unbedenkliche Abschlussfelder auf; zusätzliche Diagnosedaten werden ausdrücklich mit `WithExposure` aktiviert. Routing und Aufzeichnung beschreibt der [Streaming-Leitfaden](./docs/streaming.md#8-ag-ui-bridge).

**A2A**: `bridges/a2a` veröffentlicht einen beliebigen Runner als A2A-Server. Routing, Authentifizierung und TLS liegen beim Host:

```go
server := bridgea2a.NewServer(agent, bridgea2a.ServerOptions{
	AgentCard: bridgea2a.AgentCard{
		Name:        "Local coding agent",
		Description: "Runs coding tasks through agent-adaptor",
		Version:     "2.0.0",
		URL:         "https://host.example/a2a",
	},
	Session: bridgea2a.ThreadByContextID(), // the remote contextID maps stably to a local Thread key
	Options: []adaptor.CallOption{adaptor.WithPolicy(adaptor.PolicyWorkspaceWrite)},
})

mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
mux.Handle("/a2a", server.Handler())
```

Mit `clients/a2a` rufen Sie einen entfernten A2A-Agent auf. Der Client gibt A2A-Aufgaben, Nachrichten und Artefakte zurück und stellt eine entfernte Protokollaufgabe niemals so dar, als hätte sie stdout oder `Result` einer lokalen CLI:

```go
client := clienta2a.New(clienta2a.Options{
	AgentCardURL: "https://remote.example/.well-known/agent-card.json",
	Auth:         clienta2a.BearerTokenFromEnv("REMOTE_A2A_TOKEN"),
})
defer client.Close()

task, err := client.Send(ctx, clienta2a.SendRequest{
	Message: clienta2a.Message{
		Role:  "user",
		Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "Review this change"}},
	},
})
```

Für Zwischenschritte stehen `SendStream` und `Subscribe` bereit. `ExposurePolicy` steuert, ob Denkprozess, Tool-Aufrufe, Genehmigungsereignisse oder Diagnosefelder nach außen sichtbar werden; standardmäßig ist die Sichtbarkeit minimal. [A2A-Integration](./docs/a2a.md) beschreibt diese Steuerung, entfernte Mitglieder und Protokollgrenzen.

## Zusammenarbeit mehrerer Agents

[`hosttools/team`](./hosttools/team) verbindet einen vorrangig delegierenden Leader mit lokalen Agents, verschachtelten Teams oder entfernten A2A-Mitgliedern, die der Host auswählt. Lokale Mitglieder können verschiedene Drivers verwenden. Der Leader erhält vier feste Tools. Worker berichten über ihn; Aufgaben- und Mitgliedsmeldungen sowie Genehmigungsanfragen lokaler Worker teilen sich einen Event-Stream.

```go
func Coordinate(ctx context.Context, leader, planner, implementer *adaptor.Agent) (*adaptor.Result, error) {
	t, err := team.New(team.Config{
		Leader: leader,
		Members: []team.Member{
			{Key: "plan", Description: "Plan the change", Runner: planner},
			{Key: "impl", Description: "Implement and test", Runner: implementer},
		},
	})
	if err != nil {
		return nil, err
	}
	defer t.Close(context.Background())
	return t.Thread("issue-123").Send(ctx, "Plan and implement TASK.md").Result()
}
```

Importieren Sie `context`, `adaptor "github.com/agent-dance/agent-adaptor/v2"` und `"github.com/agent-dance/agent-adaptor/v2/hosttools/team"`. Die Agents bleiben in der Verantwortung des Hosts. Für fortlaufende Eingaben verwenden Sie `Thread(key).Standby(ctx)`; `Thread.Send` meldet bei belegtem Scope `ErrThreadBusy`. Der [Team-Leitfaden](./docs/team.md) behandelt entfernte Mitglieder, Tools, Genehmigungen, Grenzen, persistente Wiederherstellung und verzögerte Verzweigung.

Das [Live-Showcase](./examples/showcases/team-agent-workflow) führt einen lokalen Ablauf von Planung über Implementierung bis zum Review aus, mit Sandbox pro Rolle und der assistant-ui-Seite:

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
```

## Umgebungsprüfungen

`Agent.Inspect()` ist eine reine Lese-Sonde für Vorabprüfungen, Umgebungsdiagnose und Modellauswahl. Nicht unterstützte Prüfungen melden dies ausdrücklich und erfinden keine Daten:

```go
environment, err := agent.Inspect().Environment(ctx) // health, CLI version, and effective message capability
models, err := agent.Inspect().Models(ctx)
quota, err := agent.Inspect().Quota(ctx)
state, err := agent.ProfileState(ctx)                // reports desired versus observed only, changes nothing
synced, err := agent.SyncProfile(ctx)                // explicitly materializes configuration resources
```

Die eingebauten Umgebungsprüfungen melden `Version` und `Messages`. Liegt die erkannte CLI-Version unter der Mindestversion für Nachrichten, wird die Zustellung während der Ausführung deaktiviert. Wartende Nachrichten werden über eine Checkpoint-Fortsetzung verarbeitet, und die Ausführung veröffentlicht einen Hinweis. Bei unbekannter Version bleibt die deklarierte Fähigkeit erhalten; zur Laufzeit greifen die vorgesehenen Rückfallmechanismen. Siehe [Driver-Matrix](./docs/streaming.md#12-message-delivery-capabilities).

## Sechs Begriffe

Das gesamte öffentliche Modell der Bibliothek besteht aus sechs Begriffen:

| Begriff | Bedeutung |
|---|---|
| `Agent` | Ein vollständig konfigurierter Agent, der nach der Konstruktion ausführbar ist |
| `Thread` | Eine über einen Host-Schlüssel identifizierte, fortsetzbare und verzweigbare Konversation |
| `Stream` | Eine laufende Ausführung |
| `Event` | Ein streng typisiertes Ereignis während einer Ausführung |
| `Result` | Das Endergebnis und die Audit-Informationen einer Ausführung |
| `Driver` | Die Anbindung einer Agent-CLI, relevant für Erweiterungsautoren |

Dazu gelten folgende Beschränkungen: ein Konstruktor, eine Regel für die Optionszusammenführung, eine Ausführungspipeline, ein Event-Stream und eine maßgebliche Fehlerentscheidung.

## Pakete

| Paket | Zweck |
|---|---|
| [`driver`](./driver) | Driver-SPI für die Anbindung neuer Agents |
| [`codex`](./codex), [`claude`](./claude), [`cursor`](./cursor), [`codebuddy`](./codebuddy) | Eingebaute Drivers und ihre Config-Typen |
| [`tool`](./tool), [`skill`](./skill), [`mcp`](./mcp), [`profile`](./profile) | Fähigkeits- und Ressourcenvokabular für Anwender |
| [`threadstore`](./threadstore), [`memory`](./memory) | Vertrag für Thread-Persistenz und seine Implementierung im Speicher |
| [`bridges`](./bridges) | Protokollbrücken für SSE, AG-UI, A2A und subagent-stream |
| [`clients/a2a`](./clients/a2a) | A2A-Client |
| [`hosttools`](./hosttools) | Optionale Komponenten für Delegationskoordination und Event-Aufzeichnung |
| [`adaptertest`](./adaptertest) | Testsuite für Driver-Konformität |

Für die Anbindung einer eigenen Agent-CLI implementieren Sie `driver.Driver` und bringen `adaptertest` zum Bestehen. Danach stehen dieselben übergeordneten Fähigkeiten wie bei den eingebauten Drivers zur Verfügung.

## Beispiele

- [`quickstart`](./examples/quickstart): Einen Agent konstruieren und einen Prompt ausführen.
- [`streaming`](./examples/streaming): Events lesen, optional zeitgesteuert abbrechen und auf endgültigen Text zurückgreifen; [`streaming/chat`](./examples/streaming/chat) streamt eine Aufgabe auf einem benannten Thread.
- [`threads`](./examples/threads): Fortsetzen, ausschließliches Fortsetzen, Verzweigen und Checkpoint-Auditierung.
- [`structured-output`](./examples/structured-output): Typisierte JSON-Ausgabe.
- [`tools`](./examples/tools): Eine typisierte Go-Funktion einem echten lokalen Provider bereitstellen, ohne MCP selbst zu verwalten.
- [`skills`](./examples/skills): Einen Skill auswählen und seine Nachweisdatei prüfen; [`profiles`](./examples/profiles): Profilressourcen synchronisieren, optional lokal prüfen oder eine Aufgabe senden.
- [`inspect`](./examples/inspect): Zwei echte Aufrufe, anschließend Umgebungs- und Modellprüfungen, Auswahl eines deklarierten Reviewer-Skills und Beobachtung des Profilzustands.
- [`web-chat`](./examples/web-chat): SSE-/AG-UI-Server mit eingebauter Seite; das Web-Frontend ist [`assistant-ui`](./examples/web-chat/assistant-ui) mit dauerhaftem Verlauf, Werkzeugfehlern, Hinweisen während des Laufs, Freigaben und optionaler Laufbeobachtung (standardmäßig fake).
- [`a2a-server`](./examples/a2a-server): Einen Agent über A2A veröffentlichen und aufrufen.
- [`showcases/team-agent-workflow`](./examples/showcases/team-agent-workflow): Planung, Implementierung und Review in einem Ablauf.

Beispiele mit echten Aufrufen benötigen die entsprechende CLI und deren Anmeldestatus. Die regulären Tests des Repositorys erzeugen keine kostenpflichtigen Aufrufe.

## Abgrenzung

Die Kernbibliothek stellt keinen allgemeinen HTTP-/gRPC-Server, keine Host-Aufgabenwarteschlange, keinen Scheduler, keine Mandantenfähigkeit, keine Autorisierung und keine Datenbank bereit. Sie entscheidet auch nicht, welchem Agent eine Aufgabe zugewiesen wird. Protokolldienste liegen bei Bridges und Host-Anwendungen; Teamrollen und Ablaufregeln bestimmt der Host.

## Dokumentation

- [Dokumentationsübersicht](./docs/README.md)
- [API-Referenz](./docs/api-reference.md)
- [Vom Host definierte Tools](./docs/tools.md)
- [Streaming-Leitfaden](./docs/streaming.md)
- [Strukturierte Ausgabe](./docs/structured-output.md)
- [Ausführungsstrategie: Sandbox, Genehmigungen, Timeouts](./docs/run-policy.md)
- [A2A-Integration](./docs/a2a.md)
- [Öffentliche Fehler](./docs/public-errors.md)

## Migration von v1 zu v2

Verwenden Sie für alle SDK-Imports und Abhängigkeiten den Modulpfad mit `/v2`.

| v1 | v2 |
|---|---|
| `agent.Run(ctx, prompt, opts...)` | `agent.Send(ctx, prompt, opts...).Result()` |
| `agent.Stream(ctx, prompt, opts...)` | `agent.Send(ctx, prompt, opts...)` |
| `thread.Run` / `thread.Stream` | `thread.Send`, anschließend `Result()` oder `Events()` |
| `Runner` mit zwei Ausführungsmethoden | `Runner` nur mit `Send` |
| `RunFinished` als Abschluss behandeln | Das maßgebliche letzte `Done` lesen oder `Result()` aufrufen |
| Für Threads ausdrücklich einen Store einbinden | Standardmäßig Agent-lokaler Speicher; für die Wiederherstellung nach Neustarts einen persistenten Store einbinden |

`RunAs[T]` bleibt der synchrone typisierte Helfer und verwendet intern `Send`. Abonnieren Sie `Events()`, bevor Sie reguläre Fortschrittsereignisse lesen. Ein reiner Result-Aufruf hält kritische Events und eine Zusammenfassung verworfener Events zum späteren Lesen vor. Ergänzende Eingaben gehören an `Stream.Send`, fortlaufende Chats an `Thread.Standby(serverCtx)`. `Agent.Close(ctx)` gibt beim Herunterfahren weiterhin die vom Agent verwalteten Ressourcen frei.

## Lizenz

Sofern nicht anders angegeben, steht dieses Repository unter der [Apache License, Version 2.0](./LICENSE). Materialien Dritter behalten ihre jeweilige Lizenz und Namensnennung; siehe [Hinweise zu Drittmaterial](./THIRD_PARTY_NOTICES.md).

Codex, Claude, Cursor, CodeBuddy und andere Produktnamen sind Marken ihrer jeweiligen Inhaber. Sie dienen ausschließlich zur Bezeichnung unterstützter Integrationen. Dieses Projekt ist mit diesen Inhabern weder verbunden noch von ihnen unterstützt.

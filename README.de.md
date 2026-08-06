# agent-adaptor

[English](./README.md) | [简体中文](./README.zh-CN.md) | [日本語](./README.ja.md) | [한국어](./README.ko.md)

`agent-adaptor` ist ein SDK, das eine einfache, intuitive API bereitstellt, um unterschiedliche Agent-Formen wie `Codex`, `Claude Code`, `Cursor` und `CodeBuddy` einheitlich anzusteuern, und darüber hinaus zahlreiche Fähigkeiten jenseits des reinen Aufrufs bietet.

```go
agent := adaptor.New(codex.Driver(codex.Config{Model: "gpt-5.6-sol"}))
result, err := agent.Run(ctx, "Behebe die fehlschlagenden Tests")
```

Der Wechsel zu Claude Code bedeutet lediglich, den Driver in der Konstruktion auszutauschen; der übrige Code bleibt unverändert.

## Fähigkeiten im Überblick

- **Einheitliche Konfiguration**: eine API steuert skills/MCP/System-Prompts/Modell/sandbox/Tools/Genehmigungen über verschiedene Agents hinweg.
- **Streaming-Antworten**: optionale Streaming-Ausgabe, die je nach Szenario Denkprozess, Textausgabe, Tool-Aufrufe und Entscheidungsanfragen unterscheidet.
- **Konversationsverwaltung**: nahtlose Fortsetzung und Verzweigung von Konversationen. Verwenden Sie direkt Ihre eigene fachliche ID (etwa eine Ticketnummer oder Benutzer-ID) als Konversationskennung, ohne sich um die komplexen Details der zugrunde liegenden Sitzungsverwaltung kümmern zu müssen.
- **Menschliche Entscheidungen**: über Callbacks oder Events lassen sich Fragen bequem beantworten, gefährliche Befehle abfangen und Pläne bestätigen; ein eingebauter Mechanismus zum Zurückschreiben von Entscheidungen erlaubt es, Entscheidungen in der Cloud zu persistieren und nicht nur lokal.

## Erweiterte Funktionen

- **Strukturierte Ausgabe**: Sie definieren lediglich eine Go-Struktur und rufen `RunAs[T]` auf, um den Agent auszuführen und die Rückgabe auf ein vollständig gefülltes Objekt zu beschränken.
- **Dekoration mehrerer Protokolle**: eingebaute Protokolldekoration wie A2A/AGUI verpackt einen Agent mit einer Zeile Code in einen Standard-Agent mit SSE- + AGUI-Streaming-Ausgabe; zusammen mit einem eigenen fachlichen Frontend oder Client entsteht daraus ein vollwertiger Agent-Dienst (ein lauffähiges CopilotKit-Frontend ist enthalten).
- **Multi Agent**: unterstützt Team-Agent-Muster über Driver-Grenzen hinweg, etwa Codex als Leader Agent, der eigenständig einen Plan Agent (Codex), einen Coding Agent (Claude) und einen Reviewer Agent (Cursor) zur gemeinsamen Arbeit koordiniert, wobei aller Fortschritt und alle Ausgaben automatisch im Event-Stream des Leader Agent zusammenlaufen (siehe das Beispiel examples/showcases/team-agent-workflow).
- **Agent-Isolation**: unterstützt das Kopieren der lokalen Agent-Konfiguration und des Anmeldestatus in ein eigenes Verzeichnis, sodass Änderungen den lokal genutzten Agent nicht beeinflussen. Wenn Sie also mehrere Codex-/Claude-Code-Instanzen parallel für die Entwicklung erzeugen oder unterschiedliche Rollen besetzen wollen, gelingt das mühelos.

## Installation

```bash
go get github.com/agent-dance/agent-adaptor
```

Erfordert Go 1.26.5 oder neuer.

Wichtig: **zur Laufzeit muss der entsprechende Agent installiert und angemeldet sein**

## Schnellstart

```go
package main

import (
	"context"
	"fmt"
	"log"

	adaptor "github.com/agent-dance/agent-adaptor"
	"github.com/agent-dance/agent-adaptor/codex"
)

func main() {
	agent := adaptor.New(
		codex.Driver(codex.Config{Model: "gpt-5.4"}),
		adaptor.WithWorkspace("/path/to/repository"),
	)

	result, err := agent.Run(context.Background(), "Behebe die fehlschlagenden Tests")
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(result.Text)
}
```

Die vier eingebauten Driver werden auf dieselbe Weise konstruiert und bringen jeweils ihre eigene `Config` mit:

```go
codexAgent := adaptor.New(codex.Driver(codex.Config{}))
claudeAgent := adaptor.New(claude.Driver(claude.Config{}))
cursorAgent := adaptor.New(cursor.Driver(cursor.Config{}))
codeBuddyAgent := adaptor.New(codebuddy.Driver(codebuddy.Config{}))
```

## Streaming-Ausführung

`Stream` entfaltet eine Ausführung zu einem streng typisierten Event-Stream und liefert am Ende ein `Result`:

```go
stream := agent.Stream(ctx, "Erkläre den Patch, der committet werden soll")
defer stream.Cancel()

for event := range stream.Events() {
	switch event := event.(type) {
	case adaptor.TextDelta:
		fmt.Print(event.Text)
	case adaptor.Thinking:
		fmt.Fprint(os.Stderr, event.Text)
	case adaptor.ToolCall:
		if event.Phase == adaptor.PhaseStart {
			fmt.Printf("\n[Tool-Aufruf: %s]\n", event.Name)
		}
	case *adaptor.ApprovalRequest:
		_ = event.Approve(ctx)
	case adaptor.Dropped:
		log.Printf("Backpressure hat %d inkrementelle Events verworfen", event.Count)
	}
}

result, err := stream.Result()
```

Text, Denkprozess, Tool-Aufrufe und deren Ergebnisse, Prozessinformationen, Lebenszyklus, Fortschritt von Sub-Agents und Genehmigungsanfragen liegen alle in diesem einen Stream; es gibt keinen zweiten Kanal.

Wenn Sie den Konsum vorzeitig beenden, rufen Sie `Cancel()` auf; der Aufruf ist idempotent.

## Menschliche Genehmigung und sandbox

Sandbox-Stärke, Netzwerk- und Browser-Tools sowie Genehmigungsmodi liegen in derselben `Policy`; die Konstruktion setzt die Standardwerte, und bei `Run` / `Stream` lässt sich die Policy pro Aufruf vollständig überschreiben:

```go
reviewer := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithPolicy(adaptor.Policy{
		Sandbox:   adaptor.ReadOnly,    // schreibgeschützter workspace, passend für Review- und Planungsrollen
		WebSearch: adaptor.FeatureDeny, // Websuche explizit abschalten
		Browser:   adaptor.FeatureDeny,
		Approvals: adaptor.ApprovalPolicy{
			Permission: adaptor.ApprovalAsk, // gefährliche Befehle dem Menschen überlassen
			PlanReview: adaptor.ApprovalAsk,
			Question:   adaptor.QuestionAsk, // Fragen werden standardmäßig automatisch abgelehnt
			Timeout:    2 * time.Minute,
			OnTimeout:  adaptor.FallbackAbort,
		},
	}),
)
```

Die sandbox kennt die drei Stufen `ReadOnly`, `WorkspaceWrite` und `Unrestricted`; Voreinstellungen wie `PolicyReadOnly` sind lediglich Kurzformen, die allein `Sandbox` setzen. Unterstützt der gewählte Driver eine dieser Dimensionen nicht, folgt vor dem Start des Prozesses ein ausdrücklicher Fehler statt einer stillen Abschwächung.

Für Genehmigungen gibt es zwei Konsumformen, von denen Sie eine wählen. Wer einen Callback registriert, arbeitet callbackbasiert, was zu CLIs und unbeaufsichtigten Läufen passt:

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.OnApproval(func(ctx context.Context, req *adaptor.ApprovalRequest) error {
		switch req.Kind {
		case adaptor.ApprovalPermission:
			return req.Approve(ctx)
		case adaptor.ApprovalQuestion:
			return req.Answer(ctx, "Nimm PostgreSQL")
		default:
			return req.Deny(ctx, "der Plan braucht eine menschliche Bestätigung")
		}
	}),
)
```

Für unbeaufsichtigte Läufe lassen sich auch direkt die fertigen `adaptor.ApproveAll()` und `adaptor.DenyAll(reason)` verwenden.

Ohne Callback ergibt sich die eventbasierte Form: die Anfrage erscheint als `*adaptor.ApprovalRequest` im Event-Stream, bringt ihren eigenen Responder mit und kann zunächst geparkt und später von einer beliebigen Goroutine oder einem weiteren HTTP-Request beantwortet werden — genau die Form, die Web-Szenarien brauchen:

```go
for event := range stream.Events() {
	switch event := event.(type) {
	case *adaptor.ApprovalRequest:
		pending.Add(threadKey, event) // Anfrage parken und zum Rendern an das Frontend schicken
	case adaptor.Notice:
		// Das SDK verteilt jede abgeschlossene Entscheidung, inklusive automatischer Policy-Genehmigungen
		// und Timeout-Absicherungen, sodass der Host seine Liste offener Anfragen nicht selbst abgleichen muss.
		if event.Kind == adaptor.NoticeApprovalResolved {
			if id, ok := event.Data["request_id"].(string); ok {
				pending.Remove(threadKey, id)
			}
		}
	}
}
```

`pending` ist der Speicher des Hosts selbst; sobald das Frontend die Anfrage hat, trägt es die Entscheidung in einem weiteren HTTP-Request nach:

```go
func (h *host) resolveDecision(w http.ResponseWriter, r *http.Request) {
	req := h.pending.Take(threadKey, requestID)
	if err := req.Approve(r.Context()); err != nil {
		sse.WriteApprovalError(w, err) // bereits entschieden/abgelaufen → 410, Kind passt nicht → 400
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
```

Die Antwort ist exactly-once: doppelte Antworten, nicht passende Kind-Werte und bereits beendete Läufe liefern stabile Fehler (`ErrApprovalResolved`, `ErrApprovalKindMismatch`, `ErrApprovalExpired`), und auch eine Anfrage mit Nullwert blockiert nicht dauerhaft. Antwortet niemand, greift die Absicherung über `OnTimeout` aus `Policy.Approvals`; nach einer Ablehnung folgt `OnReject`. Wo geparkte Anfragen liegen, entscheidet der Host und ist nicht auf den Prozessspeicher beschränkt.

Einen vollständig lauffähigen Web-HITL-Pfad zeigt [`web-chat/copilotkit`](./examples/web-chat/copilotkit): die beiden Endpunkte `/decision/pending` und `/decision/resolve`, wobei offene Entscheidungen ein Neuladen der Seite überleben.

## Mehrstufige Konversationen

Agents sind standardmäßig zustandslos. Wenn Sie Kontinuität in der Konversation brauchen, injizieren Sie einfach einen Store:

```go
agent := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithThreadStore(memory.NewStore()),
)
defer agent.Close(context.Background())

thread := agent.Thread("tenant-42/issue-123")        // existiert die zugeordnete Konversation, wird sie fortgesetzt, andernfalls neu erstellt
result, err := thread.Run(ctx, "Untersuche dieses Problem weiter")

only := agent.Thread("tenant-42/issue-123", adaptor.ResumeOnly()) // nur fortsetzen, nicht erstellen
branch := thread.Fork("tenant-42/issue-123/plan-b")               // vom aktuellen Fortschritt verzweigen
```

Einige Konventionen:

- **Der Konversations-Key ist eine Zeichenkette des Hosts selbst**, das SDK speichert und vergleicht sie unverändert. Für eine völlig neue Konversation nehmen Sie einen neuen Key; das SDK bietet keinen Einstiegspunkt, um einen alten Key an eine neue Konversation zu binden.
- **In einem Thread läuft nur eine Ausführung gleichzeitig**, garantiert durch ein lease, sodass eine abgelaufene Ausführung neuen Zustand nicht überschreibt.
- **Vor dem Fortsetzen wird die Kompatibilität geprüft**: Driver, Modell, der aufgelöste tatsächliche workspace, Konfiguration, skills und MCP gehen alle in die fingerprint-Berechnung ein, und driftet eine dieser Angaben, wird die Konversation nicht fälschlich weiterverwendet.
- **Fehler verschmutzen den Zustand nicht**: Exits ungleich null, Protokollfehler und Abbrüche erzeugen keinen gültigen checkpoint, und der zuvor gesunde Konversationsdatensatz bleibt unverändert.
- **Residente Prozesse werden standardmäßig weiterverwendet**: Claude, CodeBuddy und Codex nutzen bei einem expliziten Thread über Runden hinweg denselben Prozess; wenn eine bestimmte oder jede Runde einen neuen Prozess braucht, ergänzen Sie `adaptor.WithSpawn()`. Cursor und zustandslose Aufrufe starten immer pro Runde einen neuen Prozess. Ausführungen nach `Close` liefern `ErrAgentClosed`.

Für Einzelprozess-Szenarien nehmen Sie `memory.NewStore()`; wird Persistenz gebraucht, implementieren Sie `threadstore.Store`.

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

Das Schema wird aus dem Go-Typ erzeugt und nutzt vorrangig die native Schema-Beschränkung des jeweiligen provider. Unterstützt der aktuelle Kanal oder die Policy das nicht, folgt automatisch der Rückfall auf Prompt-Beschränkung plus lokale Validierung; erst wenn beides nicht verfügbar ist, schlägt der Lauf vor der Ausführung fehl. Der Rückgabewert enthält sowohl den typisierten Wert als auch das vollständige `Result` für die Auditierung.

Details finden Sie im [`structured-output`-Beispiel](./examples/structured-output) und in der [Dokumentation zur strukturierten Ausgabe](./docs/structured-output.md).

## Optionen und Ressourcen

Für Optionen gibt es nur ein Vokabular; der Gültigkeitsbereich wird zur Kompilierzeit über den Typ unterschieden:

| Typ | Wo verwendbar |
|---|---|
| `Option` | nur für `adaptor.New` |
| `CallOption` | nur für `Run` / `Stream` |
| `SharedOption` | an beiden Stellen; der Aufruf überschreibt die Konstruktion |

Es gibt nur eine Zusammenführungsregel: das Nähere überschreibt das Fernere, skills werden angehängt, alles Übrige wird gemäß der jeweiligen Vereinbarung ersetzt oder zusammengeführt.

Dasselbe Set an Optionen deckt die wesentliche Konfigurationsfläche aller Agents ab:

| Was Sie steuern wollen | Womit |
|---|---|
| Modell | `WithModel` |
| System-Prompt | `WithInstructions` |
| Arbeitsverzeichnis | `WithWorkspace`, für isolierte Arbeitsbäume `WithWorkspaceSpec` |
| skills | `WithSkills` mit `skill.Dir` / `skill.FS` / `skill.Inline` / `skill.Key` / `skill.Require` |
| MCP | `WithMCP` mit `mcp.Stdio` / `mcp.HTTP` / `mcp.SSE` |
| Sandbox, Netzwerk, Browser-Tools, Genehmigungen | `WithPolicy`, interaktiv zusätzlich `OnApproval` |
| Konfigurationsverzeichnis und Ressourcen | `WithProfile`, `WithProfileResources` |
| Timeout, Audit-Metadaten, Identität des Aufrufers | `WithTimeout`, `WithMetadata`, `WithIdentity` |
| Konversationspersistenz | `WithThreadStore` |

```go
agent := adaptor.New(
	codex.Driver(codex.Config{}),
	adaptor.WithModel("gpt-5.4"),
	adaptor.WithInstructions("Du bist der Reviewer dieses Repositorys: lies den Code nur, nenne zuerst das Fazit und dann die Belege."),
	adaptor.WithSkills(skill.Dir("./skills/review")),
	adaptor.WithMCP(mcp.Stdio("repo-tools", "repo-mcp", mcp.Args("serve"))),
	adaptor.WithProfile(profile.Dedicated("./profiles/reviewer")),
	adaptor.WithTimeout(10*time.Minute),
)

result, err := agent.Run(ctx, "Prüfe diese Änderung",
	adaptor.WithModel("gpt-5.4-mini"),
	adaptor.WithSkills(skill.Require(skill.Dir("./skills/security"), "dieser Lauf muss die Sicherheitsprüfung bestehen")), // angehängt, verdrängt die Standard-skills nicht
	adaptor.WithMetadata("request_id", requestID),
)
```

Dieselbe Konfiguration mit einem anderen Driver ergibt einen anderen Agent; unterstützt ein Driver eine dieser Fähigkeiten nicht, folgt vor dem Start ein ausdrücklicher Fehler statt stillem Ignorieren.

```go
codexReviewer := adaptor.New(codex.Driver(codex.Config{}), reviewerOptions...)
claudeReviewer := adaptor.New(claude.Driver(claude.Config{}), reviewerOptions...)
```

## Host-definierte Tools

Erweitern Sie einen Agent direkt mit typisierten Go-Funktionen, ohne selbst einen MCP-Server konstruieren und pflegen zu müssen:

```go
type SearchInput struct {
	Query string `json:"query" jsonschema:"required"`
}

type SearchOutput struct {
	Files []string `json:"files"`
}

searchRepo := tool.Define(
	"search_repo",
	"Search files in the current repository.",
	func(ctx context.Context, in SearchInput) (SearchOutput, error) {
		return search(ctx, in.Query)
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

`WithTools` ist nur in der Konstruktion verwendbar und ersetzt das Tool-Set als Ganzes. Das schema wird standardmäßig aus den Go-Typen des handler abgeleitet, kann aber auch ausdrücklich als standardisiertes JSON Schema angegeben werden. `tool.Reject(code, message)` steht für einen fachlichen Fehlschlag, der dem Modell gefahrlos gezeigt werden kann, während gewöhnliche error-Werte und panics bereinigt werden. Für jedes Tool, das ein zustandsbehafteter Thread nutzt, ist `tool.Revision` zu setzen, damit Verhaltensänderungen des handler in die Kompatibilitätsprüfung beim Fortsetzen eingehen.

MCP ist hier nur der interne Auslieferungsmechanismus: bestehende oder entfernte MCP-Server laufen weiterhin über `WithMCP`, und die eingebauten Driver materialisieren Tools in ein SDK-eigenes, isoliertes profile, ohne das von Ihnen konfigurierte native profile anzufassen. Lebenszyklus, schema, Fehler, Sicherheit und Thread-Semantik beschreibt der [Vertrag für Host-definierte Tools](./docs/tools.md).

## Agent-Isolation

`WithProfile` entscheidet, welches provider-Konfigurationsverzeichnis dieser Agent nutzt. `profile.CloneNative` klont aus der nativen Konfiguration des Rechners ein eigenständiges profile und kann dabei optional settings, MCP und skills mitnehmen; der Anmeldestatus wird über eine gemeinsame Verknüpfung geteilt statt über kopierte Token:

```go
worker := adaptor.New(
	claude.Driver(claude.Config{}),
	adaptor.WithProfile(profile.CloneNative("/var/agents/worker-1",
		profile.CopySettings(),
		profile.CopyMCP(),
		profile.CopySkills(),
		profile.LinkAuth(), // teilt den lokalen Anmeldestatus per Symlink, Änderungen daran werden automatisch übernommen
	)),
)
```

So kann dieselbe CLI mehrere Instanzen je Rolle oder je Aufgabe parallel laufen lassen, deren Konfigurationsänderungen sich nicht gegenseitig beeinflussen und die auch die von Ihnen lokal genutzten `~/.claude` und `~/.codex` nicht anfassen:

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

Es gibt drei weitere Möglichkeiten: `profile.Native()` nutzt direkt die native Konfiguration des Rechners; `profile.Dedicated(dir)` verankert ein Verzeichnis, das Sie selbst verwalten; `profile.CloneFrom(src, dst, ...)` leitet aus einem Vorlagenverzeichnis ab. Das profile geht in den Konversations-fingerprint ein und kann deshalb nur eine Konstruktionsoption sein, nicht pro Aufruf gewechselt werden.

Was aus den deklarierten Ressourcen tatsächlich materialisiert wurde und ob der Driver sie wirklich anerkennt, lesen Sie mit `agent.ProfileState(ctx)` und materialisieren Sie mit `agent.SyncProfile(ctx)`; beide berichten ausschließlich tatsächlich beobachtete Ergebnisse. Eine vollständige Demonstration zeigt das [`profiles`-Beispiel](./examples/profiles).

## Ergebnisse und Fehler

Erfolg gibt `*Result, nil` zurück. Ein Fehlschlag läuft ausschließlich über den einen Weg von Gos `error`: ein Lauf, der zwar durchgelaufen ist, aber fachlich fehlgeschlagen ist, liefert einen `*RunError` mit dem verfügbaren `Result`; Fehler auf Infrastrukturebene sind gewöhnliche, umhüllbare error-Werte.

```go
result, err := agent.Run(ctx, prompt)
if err != nil {
	var runErr *adaptor.RunError
	if errors.As(err, &runErr) {
		log.Printf("Ausführung fehlgeschlagen: %s; vorhandene Zusammenfassung: %s", runErr.Reason, runErr.Result.Summary)
	}
	return err
}
```

Die einzelnen Ausgabeschichten von `Result` verschmutzen sich nicht gegenseitig:

| Feld | Inhalt |
|---|---|
| `Text` | der endgültige, an den Nutzer gerichtete Antworttext |
| `Summary` | eine kurze Zusammenfassung, passend für Listen, Logs und Issue-Kommentare |
| `Raw()` | vollständige stdout und stderr sowie die endgültige payload der jeweiligen offiziellen Protokolle |
| `Transcript()` | normalisierte Einträge, die der Driver aus dem offiziellen Protokoll geparst hat |
| `Services()` | die in dieser Ausführung tatsächlich beobachteten runtime services |
| `Decode()` | die bereits validierte strukturierte Ausgabe |
| `Usage` / `Model` / `Provider` / `Metadata` | Verbrauchs- und Audit-Informationen |

In `Text` mischt sich kein rohes stdout, und es wird auch nicht automatisch die Zusammenfassung oder die endgültige payload eines provider angehängt. Was `Run` und `Stream.Result()` liefern, ist Feld für Feld gleichwertig.

## Anbindung an übergeordnete Anwendungen

**Web-Frontend**: eine Zeile verpackt den Agent in einen `http.Handler`, der das AG-UI-Protokoll spricht, sodass AG-UI-kompatible Clients (etwa CopilotKit) sich direkt anbinden können:

```go
mux.Handle("/agent", sse.Handler(agent, sse.Options{
	Protocol: sse.AGUI,
}))
```

**A2A**: `bridges/a2a` veröffentlicht einen beliebigen Runner als A2A-Server, wobei der Host nur für Routing, Authentifizierung und TLS zuständig ist:

```go
server := bridgea2a.NewServer(agent, bridgea2a.ServerOptions{
	AgentCard: bridgea2a.AgentCard{
		Name:        "Local coding agent",
		Description: "Runs coding tasks through agent-adaptor",
		Version:     "1.0.0",
		URL:         "https://host.example/a2a",
	},
	Session: bridgea2a.ThreadByContextID(), // die entfernte contextID wird stabil auf einen lokalen Thread key abgebildet
	Options: []adaptor.CallOption{adaptor.WithPolicy(adaptor.PolicyWorkspaceWrite)},
})

mux.Handle("/.well-known/agent-card.json", server.AgentCardHandler())
mux.Handle("/a2a", server.Handler())
```

Für den umgekehrten Aufruf eines entfernten A2A-Agent dient `clients/a2a`; es gibt A2A-task, -message und -artifact zurück und tut nicht so, als hätte eine entfernte Protokollaufgabe das stdout oder `Result` einer lokalen CLI:

```go
client := clienta2a.New(clienta2a.Options{
	AgentCardURL: "https://remote.example/.well-known/agent-card.json",
	Auth:         clienta2a.BearerTokenFromEnv("REMOTE_A2A_TOKEN"),
})
defer client.Close()

task, err := client.Send(ctx, clienta2a.SendRequest{
	Message: clienta2a.Message{
		Role:  "user",
		Parts: []clienta2a.Part{{Kind: clienta2a.PartText, Text: "Prüfe diese Änderung"}},
	},
})
```

Wenn Sie die Zwischenschritte brauchen, nehmen Sie `SendStream` / `Subscribe`. Ob Denkprozess, Tool-Aufrufe, Genehmigungs-Events oder Diagnosefelder nach außen sichtbar werden, steuert `ExposurePolicy` mit minimaler Sichtbarkeit als Standard.

## Zusammenarbeit mehrerer Agents

`agent-adaptor` unterstützt die Zusammenarbeit mehrerer Agents über Driver-Grenzen hinweg auf Basis des A2A-Standardprotokolls (und damit auch mit beliebigen entfernten Agents, die A2A sprechen).

Der Wert Driver-übergreifender Zusammenarbeit liegt darin, den Passungsvorteil zwischen einem Modell und seinem nativen `Harness` zu erhalten: Modelle der GPT-Reihe schneiden auf Codex besser ab, Modelle der Claude-Reihe sind in Claude Code leistungsfähiger. Die Auslegung von `agent-adaptor` ist deshalb, jedes Modell in dem Harness mitarbeiten zu lassen, der am besten zu ihm passt, statt sich für die Ermöglichung von Zusammenarbeit über mehrere Modelle hinweg einem generischen Harness anzupassen, der viele Modelle unterstützt, aber schlecht abschneidet.

Der Kerncode sieht so aus:

```go
team, err := a2adelegation.NewService(a2adelegation.Config{
	Agents: []a2adelegation.AgentRef{
		a2adelegation.LocalNamed("plan", "Codex Planner", planner, a2adelegation.Policy{}),
		a2adelegation.LocalNamed("impl", "Claude Code Implementer", implementer, a2adelegation.Policy{}),
		a2adelegation.LocalNamed("review", "Codex Reviewer", reviewer, a2adelegation.Policy{}),
	},
})
if err != nil {
	return err
}
defer team.Close()

leader := adaptor.New(leaderDriver, team.Option())
stream := leader.Stream(ctx, "Plan, implement, and review TASK.md")
for event := range stream.Events() {
	if update, ok := event.(adaptor.SubagentUpdate); ok {
		fmt.Printf("[%s] %s: %s\n", update.Agent, update.Kind, update.Delta)
	}
}
```

Das vollständige [`team-agent-workflow`](./examples/showcases/team-agent-workflow) enthält rollenbezogene sandboxes, ein strukturiertes `PLAN.md`-Artefakt, workspace-Auditierung sowie eine CopilotKit-Seite mit Sub-Agent-Karten in Echtzeit und startet mit einem Befehl:

```bash
./examples/showcases/team-agent-workflow/start-all.sh claude
```

## Umgebungssonden

`Agent.Inspect()` ist eine reine Lese-Sonde für Prüfungen vor dem Start, Umgebungsdiagnose und Modellauswahl. Nicht unterstützte Sonden melden ausdrücklich unsupported und erfinden keine Daten:

```go
environment, err := agent.Inspect().Environment(ctx) // Gesundheitsstatus und Diagnose je Punkt, direkt darstellbar
models, err := agent.Inspect().Models(ctx)
quota, err := agent.Inspect().Quota(ctx)
state, err := agent.ProfileState(ctx)                // berichtet nur Soll und Ist, ändert nichts
synced, err := agent.SyncProfile(ctx)                // materialisiert Konfigurationsressourcen explizit
```

## Sechs Begriffe

Das öffentliche Modell der gesamten Bibliothek besteht aus nur sechs Begriffen:

| Begriff | Bedeutung |
|---|---|
| `Agent` | ein vollständig konfigurierter, nach der Konstruktion ausführbarer Agent |
| `Thread` | eine über einen fachlichen key identifizierte, fortsetzbare und verzweigbare Konversation |
| `Stream` | eine gerade laufende Ausführung |
| `Event` | ein während der Ausführung eingetretenes, streng typisiertes Ereignis |
| `Result` | das Endergebnis und die Audit-Informationen einer Ausführung |
| `Driver` | die Anbindungsimplementierung einer Agent-CLI, nur für Erweiterer relevant |

Die begleitenden Beschränkungen sind: ein Konstruktionseinstieg, ein Regelwerk für die Zusammenführung von Optionen, eine Ausführungspipeline, ein Event-Stream, ein Einstieg für die Fehlerentscheidung.

## Paketübersicht

| Paket | Zweck |
|---|---|
| [`driver`](./driver) | Driver SPI, für die Anbindung eines neuen Agent |
| [`codex`](./codex), [`claude`](./claude), [`cursor`](./cursor), [`codebuddy`](./codebuddy) | eingebaute Driver und ihre jeweilige Config |
| [`tool`](./tool), [`skill`](./skill), [`mcp`](./mcp), [`profile`](./profile) | Fähigkeits- und Ressourcenvokabular für Aufrufer |
| [`threadstore`](./threadstore), [`memory`](./memory) | Schnittstelle zur Thread-Persistenz und Implementierung im Speicher |
| [`bridges`](./bridges) | Protokollbrücken für SSE, AG-UI, A2A und subagent-stream |
| [`clients/a2a`](./clients/a2a) | A2A-Client |
| [`hosttools`](./hosttools) | optionale Komponenten für Delegationsorchestrierung und Event-Aufzeichnung |
| [`adaptertest`](./adaptertest) | Testsuite für Driver-Konformität |

So binden Sie Ihre eigene Agent-CLI an: `driver.Driver` implementieren, `adaptertest` zum Laufen bringen — danach verfügt sie über dieselben übergeordneten Fähigkeiten wie die eingebauten Driver.

## Beispiele

- [`quickstart`](./examples/quickstart): einen Agent konstruieren und einen Prompt ausführen.
- [`streaming`](./examples/streaming): Event-Konsum und Abbruch.
- [`threads`](./examples/threads): Fortsetzen, nur Fortsetzen ohne Erstellen, Verzweigen und checkpoint-Auditierung.
- [`structured-output`](./examples/structured-output): typisierte JSON-Ausgabe.
- [`tools`](./examples/tools): typisierte Go-Funktionen einem echten lokalen provider bereitstellen, ohne MCP selbst zu verwalten.
- [`skills`](./examples/skills) / [`profiles`](./examples/profiles): skill-Auflösung und -Materialisierung, Konfigurationsressourcen und Synchronisierung.
- [`inspect`](./examples/inspect): Umgebung, Modelle, Kontingent, Schema, skills und Konfigurationsstatus.
- [`web-chat`](./examples/web-chat): SSE-/AG-UI-Server mit den beiden Frontends [`aguiclient`](./examples/web-chat/aguiclient) und [`copilotkit`](./examples/web-chat/copilotkit).
- [`a2a-server`](./examples/a2a-server): einen Agent über A2A veröffentlichen und aufrufen.
- [`showcases/team-agent-workflow`](./examples/showcases/team-agent-workflow): Planung, Umsetzung und Review zu einer Pipeline verkettet.

Beispiele, die echte Aufrufe erfordern, hängen von der entsprechenden CLI und deren Anmeldestatus ab. Die regulären Tests des Repositorys erzeugen keine kostenpflichtigen Aufrufe.

## Abgrenzung

Die Kernbibliothek stellt keinen HTTP-/gRPC-Server, keine Queue, keinen Scheduler, keine Mandantenfähigkeit, keine Authentifizierung und keine Datenbank bereit und entscheidet für den Aufrufer auch nicht, welchem Agent eine Aufgabe zugewiesen werden soll. Protokolldienste bleiben bridges und übergeordneten Anwendungen überlassen, Teamrollen und Prozessstrategien der fachlichen Seite.

## Dokumentation

- [Dokumentationsübersicht](./docs/README.md)
- [API reference](./docs/api-reference.md)
- [Host-definierte Tools](./docs/tools.md)
- [Streaming-Leitfaden](./docs/streaming.md)
- [Strukturierte Ausgabe](./docs/structured-output.md)
- [Ausführungsstrategie: sandbox, Genehmigungen, Timeouts](./docs/run-policy.md)
- [A2A-Integration](./docs/a2a.md)
- [Öffentliche Fehler](./docs/public-errors.md)

## Lizenz

Sofern nicht anders angegeben, steht dieses Repository unter der
[Apache License, Version 2.0](./LICENSE). Materialien Dritter behalten ihre
jeweiligen Lizenzen und Namensnennungen; siehe
[Hinweise zu Drittmaterial](./THIRD_PARTY_NOTICES.md). Maßgeblich ist der
englische Lizenztext in `LICENSE`.

Codex, Claude, Cursor, CodeBuddy und andere Produktnamen sind Marken ihrer
jeweiligen Inhaber. Sie werden ausschließlich zur Bezeichnung unterstützter
Integrationen verwendet; dieses Projekt ist mit den Inhabern weder verbunden
noch von ihnen unterstützt.

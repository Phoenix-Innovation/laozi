// Command demo serves a local web UI that exercises every Laozi core feature:
// enforcement (severity/citation/number) + the Violations audit trail, RAG,
// the DSL test parser & SQL compiler, the human draft/approval loop, and the
// adaptive query classifier with context-limiting.
//
// Run:   cd src && go run ./cmd/demo      then open http://localhost:8080
//
// It runs fully offline: demoLLM stands in for a real model. It narrates the
// real reading and recommended range, but deliberately claims the wrong
// severity, cites an unverified source, and adds one fabricated target figure —
// so the enforcement layer's corrections (severity, citation, untraceable
// number) are visible. For a real model, swap demoLLM for
// laozi.NewDefaultLLMClient() and set LAOZI_API_KEY.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"

	laozi "github.com/Phoenix-Innovation/laozi"
)

// demoLLM misbehaves on purpose so enforcement has something to correct. It
// reads the prompt and narrates the REAL reading and the REAL recommended range
// (both traceable), but (a) always claims "success", (b) cites an unverified
// source, and (c) adds one plausible-but-fabricated "target" figure that isn't
// in the data. The engine then corrects the severity, replaces the citation
// with a registered/RAG source, and flags the fabricated figure (replacing the
// whole narration in strict mode).
type demoLLM struct{}

// cmpRe matches the engine's comparison line, e.g.
//   - fasting_glucose: 108.00 mg/dL → ABOVE maximum (70.00 - 99.00)
var cmpRe = regexp.MustCompile(`- (\S+): ([\d.]+) (\S+) → .*? \(([\d.]+) - ([\d.]+)\)`)

func (demoLLM) Chat(_ context.Context, _, user string) (string, error) {
	text := "Keeping each measurement within its recommended range supports long-term health."
	if m := cmpRe.FindStringSubmatch(user); m != nil {
		metric, val, unit, loStr, hiStr := m[1], m[2], m[3], m[4], m[5]
		lo, _ := strconv.ParseFloat(loStr, 64)
		hi, _ := strconv.ParseFloat(hiStr, 64)
		mid := (lo + hi) / 2 // realistic, but not the actual value or a bound → untraceable
		text = fmt.Sprintf(
			"Your %s reading is %s %s, against the recommended range of %s–%s %s. A reasonable personal target is around %.1f %s.",
			metric, val, unit, loStr, hiStr, unit, mid, unit)
	}
	resp, _ := json.Marshal(map[string]interface{}{
		"insight": map[string]interface{}{
			"text":      text,
			"severity":  "success", // deliberately wrong; enforced to the computed severity
			"reference": "Unverified Blog - https://made-up.example/post",
		},
	})
	return string(resp), nil
}

type app struct {
	rag         *laozi.InMemoryRAG
	classifier  *laozi.Classifier
	draftEngine *laozi.Engine // persistent, for the draft/approval demo + classification
	audit       *laozi.MemoryAuditSink
}

func baseCategories() []laozi.Category {
	return []laozi.Category{
		{
			ID: "activity", Name: "Physical Activity",
			Thresholds: []laozi.Threshold{{
				Metric: "steps", Expression: "AVG(steps) OVER(7 days)",
				Min: 8000, Max: 15000, Unit: "steps/day",
				Source: "Lancet Public Health 2022", SourceURL: "https://www.thelancet.com/journals/lanpub",
			}},
			RAGQuery: "daily steps physical activity",
		},
		{
			ID: "glucose", Name: "Metabolic Health",
			Thresholds: []laozi.Threshold{{
				Metric: "fasting_glucose", Min: 70, Max: 99, Unit: "mg/dL",
				Source: "American Diabetes Association", SourceURL: "https://diabetes.org/about-diabetes/diagnosis",
			}},
			RAGQuery: "fasting glucose diabetes",
		},
		{
			ID: "blood-pressure", Name: "Blood Pressure",
			Thresholds: []laozi.Threshold{
				{Metric: "systolic_bp", Min: 90, Max: 119, Unit: "mmHg", Source: "Harvard Health", SourceURL: "https://www.health.harvard.edu/heart-health"},
				{Metric: "diastolic_bp", Min: 60, Max: 79, Unit: "mmHg", Source: "Harvard Health", SourceURL: "https://www.health.harvard.edu/heart-health"},
			},
		},
		{
			ID: "tips", Name: "Health Tips", Educational: true,
			RAGQuery: "nutrition dietary guidelines",
		},
	}
}

func classifierDomains() []laozi.Domain {
	return []laozi.Domain{
		{Name: "activity", Description: "steps, walking, exercise, movement",
			Keywords: []string{"steps", "walk", "exercise", "active", "activity"}, Categories: []string{"activity"}},
		{Name: "metabolic", Description: "blood sugar, glucose, diabetes",
			Keywords: []string{"glucose", "sugar", "diabetes", "a1c", "metabolic"}, Categories: []string{"glucose"}},
		{Name: "cardiac", Description: "blood pressure, hypertension, heart",
			Keywords: []string{"blood pressure", "bp", "systolic", "diastolic", "hypertension", "heart"}, Categories: []string{"blood-pressure"}},
	}
}

func demoMetrics() map[string]float64 {
	return map[string]float64{"steps": 5200, "fasting_glucose": 108, "systolic_bp": 128, "diastolic_bp": 82}
}

func (a *app) seedRAG() {
	a.rag.Add(laozi.RAGResult{Content: "Adults should aim for 7,000-10,000 steps daily; 8,000+ is linked to lower mortality.", Source: "Lancet Public Health 2022", SourceURL: "https://www.thelancet.com/journals/lanpub"})
	a.rag.Add(laozi.RAGResult{Content: "Fasting glucose below 100 mg/dL is normal; 100-125 indicates prediabetes.", Source: "American Diabetes Association", SourceURL: "https://diabetes.org/about-diabetes/diagnosis"})
}

func (a *app) newEngine(strict bool) *laozi.Engine {
	e := laozi.New(
		laozi.WithLLM(demoLLM{}),
		laozi.WithRAG(a.rag),
		laozi.WithStrict(strict),
		laozi.WithAuditSink(a.audit),
		laozi.WithContext("patient", map[string]interface{}{"age": 45, "gender": "male"}),
	)
	e.AddCategories(baseCategories())
	return e
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func decode(r *http.Request, v interface{}) error {
	return json.NewDecoder(r.Body).Decode(v)
}

// POST /api/analyze  {metrics, strict} -> {insights} | {error}
func (a *app) handleAnalyze(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Metrics map[string]float64 `json:"metrics"`
		Strict  bool               `json:"strict"`
	}
	if err := decode(r, &req); err != nil {
		writeJSON(w, map[string]interface{}{"error": err.Error()})
		return
	}
	ins, err := a.newEngine(req.Strict).Analyze(r.Context(), req.Metrics)
	if err != nil {
		writeJSON(w, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"insights": ins})
}

// POST /api/classify  {message} -> {classification, categories, insights}
func (a *app) handleClassify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Message string `json:"message"`
	}
	if err := decode(r, &req); err != nil {
		writeJSON(w, map[string]interface{}{"error": err.Error()})
		return
	}
	cls := a.classifier.Classify(r.Context(), req.Message, nil)
	var cats []string
	if d, ok := a.classifier.Domain(cls.Domain); ok {
		cats = d.Categories
	}
	ins, _ := a.newEngine(false).AnalyzeSelected(r.Context(), cats, demoMetrics())
	writeJSON(w, map[string]interface{}{"classification": cls, "categories": cats, "insights": ins})
}

// POST /api/dsl  {expr} -> {valid, errors|sql}
func (a *app) handleDSL(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Expr string `json:"expr"`
	}
	if err := decode(r, &req); err != nil {
		writeJSON(w, map[string]interface{}{"error": err.Error()})
		return
	}
	errs := laozi.CheckDSL(req.Expr)
	out := map[string]interface{}{"valid": len(errs) == 0}
	if len(errs) > 0 {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Error())
		}
		out["errors"] = msgs
	} else {
		sql, _ := laozi.CompileSQL(req.Expr)
		out["sql"] = sql
	}
	writeJSON(w, out)
}

// GET /api/drafts -> {pending}
func (a *app) handleDrafts(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{"pending": a.draftEngine.PendingDrafts()})
}

// POST /api/propose  {name, metric, expression, min, max, unit, source, sourceUrl} -> {draft} | {error}
func (a *app) handlePropose(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name, Metric, Expression, Unit, Source, SourceURL, Actor string
		Min, Max                                                 float64
	}
	if err := decode(r, &req); err != nil {
		writeJSON(w, map[string]interface{}{"error": err.Error()})
		return
	}
	if req.Actor == "" {
		req.Actor = "demo-author"
	}
	cat := laozi.Category{
		ID: req.Name, Name: req.Name,
		Thresholds: []laozi.Threshold{{
			Metric: req.Metric, Expression: req.Expression,
			Min: req.Min, Max: req.Max, Unit: req.Unit,
			Source: req.Source, SourceURL: req.SourceURL,
		}},
	}
	d, err := a.draftEngine.ProposeCategory(cat, req.Actor)
	if err != nil {
		writeJSON(w, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"draft": d})
}

// POST /api/approve {id} ; POST /api/reject {id, reason}
func (a *app) handleApprove(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID, Actor string }
	_ = decode(r, &req)
	if req.Actor == "" {
		req.Actor = "demo-reviewer"
	}
	if err := a.draftEngine.ApproveDraft(req.ID, req.Actor); err != nil {
		writeJSON(w, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true, "registered": true})
}

func (a *app) handleReject(w http.ResponseWriter, r *http.Request) {
	var req struct{ ID, Actor, Reason string }
	_ = decode(r, &req)
	if req.Actor == "" {
		req.Actor = "demo-reviewer"
	}
	if err := a.draftEngine.RejectDraft(req.ID, req.Actor, req.Reason); err != nil {
		writeJSON(w, map[string]interface{}{"error": err.Error()})
		return
	}
	writeJSON(w, map[string]interface{}{"ok": true})
}

// GET /api/audit -> {entries, intact}  (durable, hash-chained audit log)
func (a *app) handleAudit(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]interface{}{"entries": a.audit.Entries(), "intact": a.audit.Verify()})
}

func (a *app) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, indexHTML)
}

func main() {
	a := &app{rag: laozi.NewInMemoryRAG(), classifier: laozi.NewClassifier(laozi.WithDomains(classifierDomains())), audit: laozi.NewMemoryAuditSink()}
	a.seedRAG()
	a.draftEngine = a.newEngine(false)

	mux := http.NewServeMux()
	mux.HandleFunc("/", a.handleIndex)
	mux.HandleFunc("/api/analyze", a.handleAnalyze)
	mux.HandleFunc("/api/classify", a.handleClassify)
	mux.HandleFunc("/api/dsl", a.handleDSL)
	mux.HandleFunc("/api/drafts", a.handleDrafts)
	mux.HandleFunc("/api/propose", a.handlePropose)
	mux.HandleFunc("/api/approve", a.handleApprove)
	mux.HandleFunc("/api/reject", a.handleReject)
	mux.HandleFunc("/api/audit", a.handleAudit)

	addr := "localhost:8080"
	log.Printf("Laozi demo running at http://%s  (Ctrl+C to stop)", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

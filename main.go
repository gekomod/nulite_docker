package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Config struct {
	Addr string
	ESPURL string
	PollInterval time.Duration
}

type App struct {
	cfg Config
	db *sql.DB
	client *http.Client
	mux *http.ServeMux
}

func env(key, fallback string) string {
	if v := strings.TrimSpace(os.Getenv(key)); v != "" { return v }
	return fallback
}

func main() {
	cfg := Config{
		Addr: env("LISTEN_ADDR", ":8080"),
		ESPURL: strings.TrimRight(env("ESP_URL", "http://192.168.31.131"), "/"),
		PollInterval: 5*time.Second,
	}
	if n := env("POLL_INTERVAL", "5s"); n != "" {
		if d, err := time.ParseDuration(n); err == nil && d >= time.Second { cfg.PollInterval = d }
	}
	db, err := sql.Open("sqlite", env("DB_PATH", "/data/nulite.db"))
	if err != nil { log.Fatal(err) }
	db.SetMaxOpenConns(1)
	if _, err = db.Exec(`PRAGMA journal_mode=WAL;
		CREATE TABLE IF NOT EXISTS samples (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			ts TEXT NOT NULL,
			comm INTEGER NOT NULL,
			uptime INTEGER,
			rssi INTEGER,
			rx_len INTEGER,
			rx_hex TEXT,
			frame_status TEXT,
			valid_frames INTEGER,
			crc_errors INTEGER,
			timeouts INTEGER,
			payload TEXT NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_samples_ts ON samples(ts);`); err != nil { log.Fatal(err) }
	a := &App{cfg:cfg, db:db, client:&http.Client{Timeout:3*time.Second}, mux:http.NewServeMux()}
	a.routes()
	ctx := context.Background()
	go a.poll(ctx)
	log.Printf("Nulite Docker listening on %s; ESP=%s; interval=%s", cfg.Addr, cfg.ESPURL, cfg.PollInterval)
	srv := &http.Server{Addr:cfg.Addr, Handler:securityHeaders(a.mux), ReadHeaderTimeout:5*time.Second}
	log.Fatal(srv.ListenAndServe())
}

func (a *App) routes() {
	a.mux.HandleFunc("/api/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok":true,"service":"nulite-docker","time":time.Now().UTC().Format(time.RFC3339)})
	})
	a.mux.HandleFunc("/api/v1/data", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet { http.Error(w,"method not allowed",405); return }
		data, status, err := a.fetchESP(r.Context(), "/api/data")
		if err != nil { writeJSON(w,502,map[string]any{"ok":false,"error":"Nie można pobrać danych z ESP","detail":err.Error()}); return }
		w.Header().Set("Content-Type","application/json; charset=utf-8")
		w.WriteHeader(status); _,_ = w.Write(data)
	})
	a.mux.HandleFunc("/api/v1/status", func(w http.ResponseWriter, r *http.Request) {
		data, _, err := a.fetchESP(r.Context(), "/api/data")
		if err != nil { writeJSON(w,200,map[string]any{"connected":false,"esp_url":a.cfg.ESPURL,"error":err.Error()}); return }
		var m map[string]any
		if json.Unmarshal(data,&m) != nil { m = map[string]any{} }
		writeJSON(w,200,map[string]any{"connected":true,"esp_url":a.cfg.ESPURL,"device":m})
	})
	a.mux.HandleFunc("/api/v1/history", func(w http.ResponseWriter, r *http.Request) {
		limit := 100
		if r.URL.Query().Get("limit") != "" {
			var n int
			if _,err:=fmtSscanf(r.URL.Query().Get("limit"),&n); err==nil && n>0 { limit=n }
		}
		if limit>1000 { limit=1000 }
		rows,err:=a.db.QueryContext(r.Context(),`SELECT ts,payload FROM samples ORDER BY id DESC LIMIT ?`,limit)
		if err!=nil { writeJSON(w,500,map[string]any{"error":err.Error()}); return }
		defer rows.Close()
		out:=make([]map[string]any,0,limit)
		for rows.Next(){ var ts,payload string; if rows.Scan(&ts,&payload)==nil { var v any; _=json.Unmarshal([]byte(payload),&v); out=append(out,map[string]any{"time":ts,"data":v}) } }
		writeJSON(w,200,map[string]any{"items":out})
	})
	a.mux.HandleFunc("/api/v1/esp/", func(w http.ResponseWriter, r *http.Request) {
		path:=strings.TrimPrefix(r.URL.Path,"/api/v1/esp")
		if path=="" || !strings.HasPrefix(path,"/") || strings.Contains(path,"..") { http.NotFound(w,r); return }
		// Proxy only known read-only routes. Firmware update and arbitrary commands are intentionally excluded.
		switch path { case "/api/data","/api/status","/api/diag": default: http.Error(w,"route not allowed",403); return }
		if r.Method!=http.MethodGet { http.Error(w,"method not allowed",405); return }
		data,status,err:=a.fetchESP(r.Context(),path)
		if err!=nil { writeJSON(w,502,map[string]any{"error":err.Error()}); return }
		w.Header().Set("Content-Type","application/json; charset=utf-8"); w.WriteHeader(status); _,_=w.Write(data)
	})
	a.mux.Handle("/", http.FileServer(http.Dir("web")))
}

func (a *App) fetchESP(ctx context.Context, path string) ([]byte,int,error) {
	req,err:=http.NewRequestWithContext(ctx,http.MethodGet,a.cfg.ESPURL+path,nil); if err!=nil{return nil,0,err}
	resp,err:=a.client.Do(req); if err!=nil{return nil,0,err}; defer resp.Body.Close()
	if resp.StatusCode<200||resp.StatusCode>=300{return nil,resp.StatusCode,errors.New(resp.Status)}
	var data json.RawMessage
	if err=json.NewDecoder(http.MaxBytesReader(nil,resp.Body,2<<20)).Decode(&data); err!=nil{return nil,0,err}
	return data,resp.StatusCode,nil
}

func (a *App) poll(ctx context.Context) {
	t:=time.NewTicker(a.cfg.PollInterval); defer t.Stop()
	for { a.storeSample(ctx); select { case <-ctx.Done(): return; case <-t.C: } }
}
func (a *App) storeSample(ctx context.Context) {
	data,_,err:=a.fetchESP(ctx,"/api/data"); if err!=nil { log.Printf("ESP poll: %v",err); return }
	var d struct {
		Comm bool `json:"comm"`; Uptime int64 `json:"uptime"`; RSSI int `json:"rssi"`; RxLen int `json:"rxLen"`; RxHex string `json:"rxHex"`; FrameStatus string `json:"frameStatus"`; ValidFrames int `json:"validFrames"`; CRCErrors int `json:"crcErrors"`; Timeouts int `json:"timeouts"`
	}
	if err:=json.Unmarshal(data,&d); err!=nil { log.Printf("ESP JSON: %v",err); return }
	comm:=0; if d.Comm {comm=1}
	_,err=a.db.ExecContext(ctx,`INSERT INTO samples(ts,comm,uptime,rssi,rx_len,rx_hex,frame_status,valid_frames,crc_errors,timeouts,payload) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		time.Now().UTC().Format(time.RFC3339),comm,d.Uptime,d.RSSI,d.RxLen,d.RxHex,d.FrameStatus,d.ValidFrames,d.CRCErrors,d.Timeouts,string(data))
	if err!=nil {log.Printf("SQLite insert: %v",err)}
}
func writeJSON(w http.ResponseWriter,status int,v any){w.Header().Set("Content-Type","application/json; charset=utf-8");w.Header().Set("Cache-Control","no-store");w.WriteHeader(status);_=json.NewEncoder(w).Encode(v)}
func securityHeaders(next http.Handler) http.Handler { return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.Header().Set("X-Content-Type-Options","nosniff");w.Header().Set("X-Frame-Options","DENY");w.Header().Set("Referrer-Policy","same-origin");next.ServeHTTP(w,r)}) }

// Tiny helper avoids accepting arbitrary SQL parameters from request text.
func fmtSscanf(s string, n *int) (int,error) {
	v:=0; seen:=false
	for _,c:=range s { if c<'0'||c>'9' {return 0,errors.New("invalid integer")}; seen=true; v=v*10+int(c-'0'); if v>1000000{return 0,errors.New("too large")} }
	if !seen{return 0,errors.New("empty")}; *n=v; return 1,nil
}

package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

type Config struct { Addr string; PollInterval time.Duration }
type App struct { cfg Config; db *sql.DB; client *http.Client; mux *http.ServeMux }

func env(key, fallback string) string { if v:=strings.TrimSpace(os.Getenv(key)); v!="" { return v }; return fallback }
func main() {
	cfg:=Config{Addr:env("LISTEN_ADDR",":8080"),PollInterval:5*time.Second}
	if d,e:=time.ParseDuration(env("POLL_INTERVAL","5s")); e==nil && d>=time.Second { cfg.PollInterval=d }
	db,e:=sql.Open("sqlite",env("DB_PATH","/data/nulite.db")); if e!=nil {log.Fatal(e)}
	db.SetMaxOpenConns(1)
	_,e=db.Exec(`PRAGMA journal_mode=WAL;
	CREATE TABLE IF NOT EXISTS settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
	CREATE TABLE IF NOT EXISTS samples (id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, comm INTEGER NOT NULL, uptime INTEGER, rssi INTEGER, rx_len INTEGER, rx_hex TEXT, frame_status TEXT, valid_frames INTEGER, crc_errors INTEGER, timeouts INTEGER, payload TEXT NOT NULL);
	CREATE INDEX IF NOT EXISTS idx_samples_ts ON samples(ts);`); if e!=nil {log.Fatal(e)}
	// Environment variable is a convenient initial value; saved settings in SQLite take precedence.
	if v:=strings.TrimRight(strings.TrimSpace(os.Getenv("ESP_URL")),"/"); v!="" { _,e=db.Exec(`INSERT INTO settings(key,value) VALUES('esp_url',?) ON CONFLICT(key) DO NOTHING`,v); if e!=nil {log.Fatal(e)} }
	a:=&App{cfg:cfg,db:db,client:&http.Client{Timeout:4*time.Second},mux:http.NewServeMux()}
	a.routes()
	go a.poll(context.Background())
	log.Printf("Nulite Docker listening on %s",cfg.Addr)
	log.Fatal((&http.Server{Addr:cfg.Addr,Handler:securityHeaders(a.mux),ReadHeaderTimeout:5*time.Second}).ListenAndServe())
}
func (a *App) getESPURL(ctx context.Context) string { var v string; _=a.db.QueryRowContext(ctx,"SELECT value FROM settings WHERE key='esp_url'").Scan(&v); return strings.TrimRight(strings.TrimSpace(v),"/") }
func (a *App) configured(ctx context.Context) bool { return a.getESPURL(ctx)!="" }
func (a *App) routes() {
	a.mux.HandleFunc("/api/v1/health",func(w http.ResponseWriter,r *http.Request){writeJSON(w,200,map[string]any{"ok":true,"service":"nulite-docker","time":time.Now().UTC().Format(time.RFC3339)})})
	a.mux.HandleFunc("/api/v1/config",func(w http.ResponseWriter,r *http.Request){
		if r.Method==http.MethodGet { var configured bool; url:=a.getESPURL(r.Context()); configured=url!=""; if url!="" { u:=strings.TrimRight(url,"/"); if len(u)>48 {u=u[:48]+"…"}; writeJSON(w,200,map[string]any{"configured":true,"esp_url":u}); return }; writeJSON(w,200,map[string]any{"configured":false,"esp_url":""}); return }
		if r.Method!=http.MethodPost {http.Error(w,"method not allowed",405);return}
		r.Body=http.MaxBytesReader(w,r.Body,4096); var input struct{ ESPURL string `json:"esp_url"` }
		if e:=json.NewDecoder(r.Body).Decode(&input); e!=nil {writeJSON(w,400,map[string]any{"error":"Nieprawidłowy JSON"});return}
		url:=strings.TrimRight(strings.TrimSpace(input.ESPURL),"/")
		if !(strings.HasPrefix(url,"http://")||strings.HasPrefix(url,"https://")) || strings.ContainsAny(url," \r\n\t") {writeJSON(w,400,map[string]any{"error":"Podaj poprawny adres http:// lub https:// urządzenia ESP"});return}
		// Verify reachability and JSON response before saving, so first-run setup cannot save a typo.
		ctx,cancel:=context.WithTimeout(r.Context(),5*time.Second); defer cancel()
		if _,_,e:=a.fetchURL(ctx,url,"/api/data"); e!=nil {writeJSON(w,400,map[string]any{"error":"Nie udało się połączyć z ESP przez /api/data: "+e.Error()});return}
		if _,e:=a.db.ExecContext(r.Context(),`INSERT INTO settings(key,value) VALUES('esp_url',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`,url); e!=nil {writeJSON(w,500,map[string]any{"error":"Nie udało się zapisać konfiguracji"});return}
		writeJSON(w,200,map[string]any{"ok":true,"configured":true})
	})
	a.mux.HandleFunc("/setup",func(w http.ResponseWriter,r *http.Request){http.Redirect(w,r,"/",http.StatusSeeOther)})
	a.mux.HandleFunc("/api/v1/status",func(w http.ResponseWriter,r *http.Request){
		url:=a.getESPURL(r.Context()); if url=="" {writeJSON(w,200,map[string]any{"configured":false,"connected":false});return}
		data,_,e:=a.fetchURL(r.Context(),url,"/api/data"); if e!=nil {writeJSON(w,200,map[string]any{"configured":true,"connected":false,"error":e.Error()});return}
		var d any; _=json.Unmarshal(data,&d); writeJSON(w,200,map[string]any{"configured":true,"connected":true,"esp_url":url,"device":d})
	})
	a.mux.HandleFunc("/api/v1/data",func(w http.ResponseWriter,r *http.Request){
		url:=a.getESPURL(r.Context()); if url=="" {writeJSON(w,428,map[string]any{"configured":false,"error":"Najpierw skonfiguruj adres ESP"});return}
		data,status,e:=a.fetchURL(r.Context(),url,"/api/data"); if e!=nil {writeJSON(w,502,map[string]any{"ok":false,"error":"Nie można pobrać danych z ESP","detail":e.Error()});return}
		w.Header().Set("Content-Type","application/json; charset=utf-8");w.WriteHeader(status);_,_=w.Write(data)
	})
	a.mux.HandleFunc("/api/v1/history",func(w http.ResponseWriter,r *http.Request){
		limit:=100; if s:=r.URL.Query().Get("limit");s!="" {n,e:=parseInt(s);if e==nil&&n>0{limit=n}};if limit>1000{limit=1000}
		rows,e:=a.db.QueryContext(r.Context(),`SELECT ts,payload FROM samples ORDER BY id DESC LIMIT ?`,limit);if e!=nil{writeJSON(w,500,map[string]any{"error":e.Error()});return};defer rows.Close()
		out:=make([]map[string]any,0,limit);for rows.Next(){var ts,payload string;if rows.Scan(&ts,&payload)==nil{var v any;_=json.Unmarshal([]byte(payload),&v);out=append(out,map[string]any{"time":ts,"data":v})}}
		writeJSON(w,200,map[string]any{"items":out})
	})
	a.mux.HandleFunc("/api/v1/esp/",func(w http.ResponseWriter,r *http.Request){
		path:=strings.TrimPrefix(r.URL.Path,"/api/v1/esp")
		switch path { case "/api/data","/api/status","/api/diag","/api/v1/config/device","/api/v1/log","/api/v1/diagnostics","/api/v1/scan","/api/v1/restart","/update": default: http.Error(w,"route not allowed",403); return }
		if path=="/update" {
			if r.Method!=http.MethodPost { http.Error(w,"method not allowed",405); return }
		} else if path=="/api/v1/scan" {
			if r.Method!=http.MethodPost { http.Error(w,"method not allowed",405); return }
		} else if path=="/api/v1/config/device" {
			if r.Method!=http.MethodGet && r.Method!=http.MethodPost { http.Error(w,"method not allowed",405); return }
		} else if r.Method!=http.MethodGet { http.Error(w,"method not allowed",405); return }
		url:=a.getESPURL(r.Context()); if url=="" { writeJSON(w,428,map[string]any{"error":"Najpierw skonfiguruj adres ESP"}); return }
		ctx,cancel:=context.WithTimeout(r.Context(),2*time.Minute); defer cancel()
		req,e:=http.NewRequestWithContext(ctx,r.Method,strings.TrimRight(url,"/")+path,r.Body); if e!=nil { http.Error(w,"invalid request",400); return }
		for _,h:=range []string{"Content-Type","Content-Length","Authorization"} { if v:=r.Header.Get(h);v!="" { req.Header.Set(h,v) } }
		proxyClient:=a.client; if path=="/update" { proxyClient=&http.Client{Timeout:2*time.Minute} }; resp,e:=proxyClient.Do(req); if e!=nil { writeJSON(w,502,map[string]any{"error":"Błąd połączenia z ESP","detail":e.Error()}); return }; defer resp.Body.Close()
		for _,h:=range []string{"Content-Type","Cache-Control"} { if v:=resp.Header.Get(h);v!="" { w.Header().Set(h,v) } }
		w.WriteHeader(resp.StatusCode); _,_=io.Copy(w,io.LimitReader(resp.Body,8<<20))
	})
	a.mux.HandleFunc("/",func(w http.ResponseWriter,r *http.Request){
		if r.URL.Path!="/" {http.NotFound(w,r);return}
		if !a.configured(r.Context()) {http.ServeFile(w,r,"web/setup.html");return}
		http.ServeFile(w,r,"web/index.html")
	})
}
func (a *App) fetchURL(ctx context.Context,base,path string)([]byte,int,error){
	req,e:=http.NewRequestWithContext(ctx,http.MethodGet,strings.TrimRight(base,"/")+path,nil);if e!=nil{return nil,0,e}
	resp,e:=a.client.Do(req);if e!=nil{return nil,0,e};defer resp.Body.Close()
	if resp.StatusCode<200||resp.StatusCode>=300{return nil,resp.StatusCode,errors.New(resp.Status)}
	data,e:=io.ReadAll(io.LimitReader(resp.Body,2<<20));if e!=nil{return nil,0,e}
	if !json.Valid(data){return nil,0,errors.New("ESP zwróciło nieprawidłowy JSON")};return data,resp.StatusCode,nil
}
func (a *App) poll(ctx context.Context){t:=time.NewTicker(a.cfg.PollInterval);defer t.Stop();for{a.storeSample(ctx);select{case<-ctx.Done():return;case<-t.C:}}}
func (a *App) storeSample(ctx context.Context){
	url:=a.getESPURL(ctx);if url==""{return};data,_,e:=a.fetchURL(ctx,url,"/api/data");if e!=nil{log.Printf("ESP poll: %v",e);return}
	var d struct{Comm bool `json:"comm"`;Uptime int64 `json:"uptime"`;RSSI int `json:"rssi"`;RxLen int `json:"rxLen"`;RxHex string `json:"rxHex"`;FrameStatus string `json:"frameStatus"`;ValidFrames int `json:"validFrames"`;CRCErrors int `json:"crcErrors"`;Timeouts int `json:"timeouts"`}
	if e=json.Unmarshal(data,&d);e!=nil{log.Printf("ESP JSON: %v",e);return};comm:=0;if d.Comm{comm=1}
	_,e=a.db.ExecContext(ctx,`INSERT INTO samples(ts,comm,uptime,rssi,rx_len,rx_hex,frame_status,valid_frames,crc_errors,timeouts,payload) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,time.Now().UTC().Format(time.RFC3339),comm,d.Uptime,d.RSSI,d.RxLen,d.RxHex,d.FrameStatus,d.ValidFrames,d.CRCErrors,d.Timeouts,string(data));if e!=nil{log.Printf("SQLite insert: %v",e)}
}
func parseInt(s string)(int,error){v:=0;if s==""{return 0,errors.New("empty")};for _,c:=range s{if c<'0'||c>'9'{return 0,errors.New("invalid integer")};v=v*10+int(c-'0');if v>1000000{return 0,errors.New("too large")}};return v,nil}
func writeJSON(w http.ResponseWriter,status int,v any){w.Header().Set("Content-Type","application/json; charset=utf-8");w.Header().Set("Cache-Control","no-store");w.WriteHeader(status);_=json.NewEncoder(w).Encode(v)}
func securityHeaders(next http.Handler)http.Handler{return http.HandlerFunc(func(w http.ResponseWriter,r *http.Request){w.Header().Set("X-Content-Type-Options","nosniff");w.Header().Set("X-Frame-Options","DENY");w.Header().Set("Referrer-Policy","same-origin");next.ServeHTTP(w,r)})}

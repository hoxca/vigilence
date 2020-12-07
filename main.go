package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"

	Log "github.com/apatters/go-conlog"
	"github.com/gofrs/uuid"
	"github.com/gorilla/websocket"
	ps "github.com/mitchellh/go-ps"
)

var addr = flag.String("addr", "127.0.0.1:5950", "voyager tcp server address")
var verbosity = flag.String("level", "warn", "set log level of clandestine default warn")
var heartbeat event
var counterr = 0

type loglevel int

const (
	debug loglevel = iota
	info
	warning
	critical
	title
	subtitle
	evnt
	request
	emergency
)

func (l loglevel) String() string {
	return [...]string{
		"DEBUG",
		"INFO",
		"WARNING",
		"CRITICAL",
		"TITLE",
		"SUBTITLE",
		"EVENT",
		"REQUEST",
		"EMERGENCY",
	}[l-1]
}

type event struct {
	Event     string  `json:"Event"`
	Timestamp float64 `json:"Timestamp"`
	Host      string  `json:"Host,omitempty"`
	Inst      int     `json:"Inst"`
}

type logevent struct {
	Event     string  `json:"Event"`
	Timestamp float64 `json:"Timestamp"`
	Host      string  `json:"Host"`
	Inst      int     `json:"Inst"`
	TimeInfo  float64 `json:"TimeInfo"`
	Type      int     `json:"Type"`
	Text      string  `json:"Text"`
}

type controldata struct {
	Event       string  `json:"Event"`
	Timestamp   float64 `json:"Timestamp"`
	Host        string  `json:"Host"`
	Inst        int     `json:"Inst"`
	TI          string  `json:"TI"`
	VOYSTAT     int     `json:"VOYSTAT"`
	SETUPCONN   bool    `json:"SETUPCONN"`
	CCDCONN     bool    `json:"CCDCONN"`
	CCDTEMP     float64 `json:"CCDTEMP"`
	CCDPOW      int     `json:"CCDPOW"`
	CCDSETP     int     `json:"CCDSETP"`
	CCDCOOL     bool    `json:"CCDCOOL"`
	CCDSTAT     int     `json:"CCDSTAT"`
	MNTCONN     bool    `json:"MNTCONN"`
	MNTPARK     bool    `json:"MNTPARK"`
	MNTRA       string  `json:"MNTRA"`
	MNTDEC      string  `json:"MNTDEC"`
	MNTRAJ2000  string  `json:"MNTRAJ2000"`
	MNTDECJ2000 string  `json:"MNTDECJ2000"`
	MNTAZ       string  `json:"MNTAZ"`
	MNTALT      string  `json:"MNTALT"`
	MNTPIER     string  `json:"MNTPIER"`
	MNTTFLIP    string  `json:"MNTTFLIP"`
	MNTSFLIP    int     `json:"MNTSFLIP"`
	MNTTRACK    bool    `json:"MNTTRACK"`
	MNTSLEW     bool    `json:"MNTSLEW"`
	AFCONN      bool    `json:"AFCONN"`
	AFTEMP      float64 `json:"AFTEMP"`
	AFPOS       int     `json:"AFPOS"`
	SEQTOT      int     `json:"SEQTOT"`
	SEQPARZ     int     `json:"SEQPARZ"`
	GUIDECONN   bool    `json:"GUIDECONN"`
	GUIDESTAT   int     `json:"GUIDESTAT"`
	DITHSTAT    int     `json:"DITHSTAT"`
	GUIDEX      float64 `json:"GUIDEX"`
	GUIDEY      float64 `json:"GUIDEY"`
	PLACONN     bool    `json:"PLACONN"`
	PSCONN      bool    `json:"PSCONN"`
	SEQNAME     string  `json:"SEQNAME"`
	SEQSTART    string  `json:"SEQSTART"`
	SEQREMAIN   string  `json:"SEQREMAIN"`
	SEQEND      string  `json:"SEQEND"`
	RUNSEQ      string  `json:"RUNSEQ"`
	RUNDS       string  `json:"RUNDS"`
	ROTCONN     bool    `json:"ROTCONN"`
	ROTPA       int     `json:"ROTPA"`
	ROTSKYPA    int     `json:"ROTSKYPA"`
	ROTISROT    bool    `json:"ROTISROT"`
	DRAGRUNNING bool
	SEQRUNNING  bool
}

var controlDataUpdated = false

type method struct {
	Method string `json:"method"`
	Params params `json:"params"`
	ID     int    `json:"id"`
}

type params struct {
	UID         string `json:"UID"`
	IsOn        bool   `json:"IsOn"`
	Level       *int   `json:"Level,omitempty"`
	IsHalt      bool   `json:"IsHalt,omitempty"`
	CommandType int    `json:"CommandType,omitempty"`
	IsSetPoint  bool   `json:"IsSetPoint,omitempty"`
	IsCoolDown  bool   `json:"IsCoolDown,omitempty"`
	IsASync     bool   `json:"IsASync,omitempty"`
	IsWarmup    bool   `json:"IsWarmup,omitempty"`
	IsCoolerOFF bool   `json:"IsCoolerOFF,omitempty"`
	Temperature int    `json:"Temperature,omitempty"`
}

var voyagerStatus controldata
var emergencyManaged = false

var timeout = time.Duration(1)
var done chan bool
var quit chan bool

func main() {
	flag.Parse()
	setUpLogs()

	c, errcon := connectVoyager(addr)
	if errcon != nil {
		Log.Debugf("Voyager is not running or is not responding !\n")
		fmt.Println("Unreachable")
		os.Exit(1)
	}
	defer c.Close()

	quit = make(chan bool)
	done = make(chan bool)

	go recvFromVoyager(c, done)
	remoteSetDashboard(c)
	go heartbeatVoyager(c, quit)

	for {
		if controlDataUpdated && !emergencyManaged {
			voyagerStatusDebug()
			emergencyLogic(c, quit)
		}
		if emergencyManaged {
			Log.Debugf("Emergency managed")
			break
		}
		// Log.Debugf("Main loop")
		time.Sleep(150 * time.Millisecond)
	}

	// fmt.Println("that's all folks")
}

func voyagerStatusDebug() {
	if controlDataUpdated {
		Log.Debugf("Voyager Status:")
		Log.Debugf("  Voyager    status: %d", voyagerStatus.VOYSTAT)
		Log.Debugf("  Voyager connected: %s", strconv.FormatBool(voyagerStatus.SETUPCONN))
		Log.Debugf("  Mount   connected: %s", strconv.FormatBool(voyagerStatus.MNTCONN))
		Log.Debugf("  Mount      parked: %s\n", strconv.FormatBool(voyagerStatus.MNTPARK))
	}
}

func emergencyLogic(c *websocket.Conn, quit chan bool) {

	if controlDataUpdated {

		if voyagerStatus.MNTCONN && voyagerStatus.SEQRUNNING && !voyagerStatus.DRAGRUNNING {
			Log.Debugln("Voyager is on the fly, must stop sequence, park mount and return")
			fmt.Println("OntheFly")
			remoteAbort(c)
			time.Sleep(3 * time.Second)
			if voyagerStatus.CCDCOOL && voyagerStatus.CCDCONN {
				remoteWarming(c)
			}
			time.Sleep(1 * time.Second)
			if !voyagerStatus.MNTPARK {
				remotePark(c)
			}
			time.Sleep(2 * time.Second)
		}

		if voyagerStatus.MNTCONN && voyagerStatus.SEQRUNNING && voyagerStatus.DRAGRUNNING {
			Log.Debugln("Voyager dragscript and sequence are running, let's voyager manage emergency")
			fmt.Println("Dragscript")
		}

		if voyagerStatus.MNTCONN && !voyagerStatus.SEQRUNNING && voyagerStatus.DRAGRUNNING {
			Log.Debugln("Voyager dragscript is running, let's voyager manage emergency")
			fmt.Println("Dragscript")
		}

		if voyagerStatus.MNTCONN && !voyagerStatus.SEQRUNNING && !voyagerStatus.DRAGRUNNING {
			Log.Debugln("Voyager idle and mount connected, must park mount and return")
			fmt.Println("IdleConnected")
			if voyagerStatus.CCDCOOL && voyagerStatus.CCDCONN {
				remoteWarming(c)
			}
			time.Sleep(1 * time.Second)
			if !voyagerStatus.MNTPARK {
				remotePark(c)
			}
			time.Sleep(2 * time.Second)
		}

		if !voyagerStatus.SETUPCONN {
			Log.Debugln("Voyager not connected => Talon will manage parking")
			fmt.Println("NotConnected")
		}

		// should neve happen but just in case
		if voyagerStatus.SETUPCONN && !voyagerStatus.MNTCONN {
			Log.Debugln("Voyager connected, mount not connected => Talon will manage parking")
			fmt.Println("NotConnected")
		}

		done <- true
		time.Sleep(1 * time.Second)
		emergencyManaged = true
	}

}

func recvFromVoyager(c *websocket.Conn, done chan bool) {
	for {
		select {
		case <-done:
			Log.Debugf("Quit recv loop!")
			quit <- true
			return
		default:
			_, message, err := c.ReadMessage()
			if err != nil {
				Log.Warn("read:", err)
				quit <- true
				return
			}
			// parse incoming message
			msg := string(message)
			switch {
			case strings.Contains(msg, `"Event":"ControlData"`):
				if !controlDataUpdated {
					Log.Debugf("recv msg: %s", strings.TrimRight(msg, "\r\n"))
					voyagerStatus = parseControlData(message)
				}
			case strings.Contains(msg, `"Event":"LogEvent"`):
				ts, level, logline := parseLogEvent(message)
				Log.Debugf("recv log: %.5f %s %s", ts, level, logline)
			case strings.Contains(msg, `"Event":"RemoteActionResult"`):
				Log.Debugf("recv result: %s", strings.TrimRight(msg, "\r\n"))
			case strings.Contains(msg, `"Event":"Version"`):
				Log.Debugf("recv version: %s", strings.TrimRight(msg, "\r\n"))
			case strings.Contains(msg, `"Event":"VikingManaged"`):
				Log.Debugf("recv viking: %s", strings.TrimRight(msg, "\r\n"))
			default:
				Log.Debugf("recv not managed: %s", strings.TrimRight(msg, "\r\n"))
			}
		}
		time.Sleep(50 * time.Millisecond)
		// Log.Debugf("RCV loop")
	}
}

func processAlreadyRunning(pname string) bool {
	pid := os.Getpid()
	process, _ := ps.Processes()
	for _, p := range process {
		if p.Executable() == pname && p.Pid() != pid {
			fmt.Printf("%s: %d\n", p.Executable(), p.Pid())
			return true
		}
	}
	return false
}

func parseLogEvent(message []byte) (float64, string, string) {
	type logEvent struct {
		Event     string   `json:"Event"`
		Timestamp float64  `json:"Timestamp"`
		Host      string   `json:"Host"`
		Inst      int      `json:"Inst"`
		TimeInfo  float64  `json:"TimeInfo"`
		Type      loglevel `json:"Type"`
		Text      string   `json:"Text"`
	}

	var e logEvent
	err := json.Unmarshal([]byte(message), &e)
	if err != nil {
		Log.Warn("Cannot parse logEvent: %s", err)
	}

	return e.TimeInfo, e.Type.String(), e.Text
}

func parseControlData(message []byte) controldata {
	var cdata controldata
	err := json.Unmarshal([]byte(message), &cdata)
	if err != nil {
		Log.Warn("Cannot parse controlData: %s", err)
	}

	if cdata.RUNSEQ == "" {
		Log.Debugln("Sequence   running: false")
		cdata.SEQRUNNING = false
	} else {
		Log.Debugf("Sequence   running: true; sequence: %s", cdata.RUNSEQ)
		cdata.SEQRUNNING = true
	}
	if cdata.RUNDS == "" {
		Log.Debugln("Dragscript running: false")
		cdata.DRAGRUNNING = false
	} else {
		Log.Debugf("Dragscript running: true; dragscript: %s", cdata.RUNDS)
		cdata.DRAGRUNNING = true
	}
	controlDataUpdated = true
	return cdata
}

func sendPollingMsg(c *websocket.Conn) {
	secs := time.Now().Unix()
	heartbeat := &event{
		Event:     "Polling",
		Timestamp: float64(secs),
		Inst:      1,
	}
	data, _ := json.Marshal(heartbeat)
	sendToVoyager(c, data)
}

var lastpoll time.Time

func heartbeatVoyager(c *websocket.Conn, quit chan bool) {

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)

	lastpoll = time.Now()

	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()

	for {
		select {
		case t := <-ticker.C:
			now := t
			elapsed := now.Sub(lastpoll)

			// manage heartbeat
			if elapsed.Seconds() > 10 {
				lastpoll = now
				secs := now.Unix()
				heartbeat := &event{
					Event:     "Polling",
					Timestamp: float64(secs),
					Inst:      1,
				}
				data, _ := json.Marshal(heartbeat)
				sendToVoyager(c, data)
			}
		case <-quit:
			Log.Debugf("Quit heartbeat loop!")
			err := c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				Log.Println("write close:", err)
				return
			}
			return
		case <-interrupt:
			Log.Debugf("Want interrupt!")
			// Close the read goroutine
			done <- true
			// Cleanly close the websocket connection by sending a close message
			err := c.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				Log.Warn("write close:", err)
				return
			}
			Log.Println("Shutdown vigilence")
			os.Exit(0)
		}
	}
}

func connectVoyager(addr *string) (*websocket.Conn, error) {
	u := url.URL{Scheme: "ws", Host: *addr, Path: "/"}
	Log.Debugf("connecting to %s", u.String())

	websocket.DefaultDialer.HandshakeTimeout = timeout * time.Second
	c, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		Log.Printf("Can't connect, verify Voyager address or tcp port in the Voyager configuration\n")
		// Log.Fatal("Critical: ", err)
	}
	return c, err
}

func askForLog(c *websocket.Conn) {
	//	time.Sleep(1 * time.Second)
	level := 0
	p := &params{
		UID:   fmt.Sprintf("%s", uuid.Must(uuid.NewV4())),
		IsOn:  true,
		Level: &level,
	}

	askLog := &method{
		Method: "RemoteSetLogEvent",
		Params: *p,
		ID:     1,
	}

	data, _ := json.Marshal(askLog)
	sendToVoyager(c, data)
}

func remoteSetDashboard(c *websocket.Conn) {
	//	time.Sleep(2 * time.Second)

	p := &params{
		UID:  fmt.Sprintf("%s", uuid.Must(uuid.NewV4())),
		IsOn: true,
	}

	setDashboard := &method{
		Method: "RemoteSetDashboardMode",
		Params: *p,
		ID:     2,
	}

	data, _ := json.Marshal(setDashboard)
	sendToVoyager(c, data)
}

func remoteAbort(c *websocket.Conn) {
	p := &params{
		IsHalt: true,
	}

	abortHaltAll := &method{
		Method: "Abort",
		Params: *p,
		ID:     3,
	}

	data, _ := json.Marshal(abortHaltAll)
	sendToVoyager(c, data)
}

func remoteWarming(c *websocket.Conn) {
	p := &params{
		IsASync:     true,
		IsWarmup:    true,
		IsCoolerOFF: false,
	}

	warmCamera := &method{
		Method: "RemoteCooling",
		Params: *p,
		ID:     3,
	}

	data, _ := json.Marshal(warmCamera)
	sendToVoyager(c, data)
}

func remotePark(c *websocket.Conn) {
	p := &params{
		UID:         fmt.Sprintf("%s", uuid.Must(uuid.NewV4())),
		CommandType: 3,
	}

	parkMount := &method{
		Method: "RemoteMountFastCommand",
		Params: *p,
		ID:     4,
	}

	data, _ := json.Marshal(parkMount)
	sendToVoyager(c, data)
}

func sendToVoyager(c *websocket.Conn, data []byte) {
	lastpoll = time.Now()
	err := c.WriteMessage(websocket.TextMessage, []byte(fmt.Sprintf("%s\r\n", data)))
	if err != nil {
		Log.Println("write:", err)
		return
	}
	Log.Debugf("send: %s", data)
	time.Sleep(1 * time.Second)
}

func currentDateLog() string {
	var d string
	t := time.Now()
	loc, _ := time.LoadLocation("Europe/Paris")

	switch {
	case t.In(loc).Hour() < 12:
		d = fmt.Sprintf("%s", t.AddDate(0, 0, -1).Format("2006-01-02"))
	default:
		d = fmt.Sprintf("%s", t.Format("2006-01-02"))
	}
	return d
}

func setUpLogs() {
	formatter := Log.NewStdFormatter()
	formatter.Options.TimestampType = Log.TimestampTypeWall
	formatter.Options.LogLevelFmt = Log.LogLevelFormatLongTitle
	formatter.Options.WallclockTimestampFmt = time.ANSIC
	Log.SetFormatter(formatter)
	switch *verbosity {
	case "debug":
		Log.SetLevel(Log.DebugLevel)
	case "info":
		Log.SetLevel(Log.InfoLevel)
	case "warn":
		Log.SetLevel(Log.WarnLevel)
	case "error":
		Log.SetLevel(Log.ErrorLevel)
	default:
		Log.SetLevel(Log.WarnLevel)
	}
}

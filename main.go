package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"cloud.google.com/go/firestore"
	speech "cloud.google.com/go/speech/apiv1"
	"github.com/gorilla/websocket"
	"golang.org/x/net/context"
	speechpb "google.golang.org/genproto/googleapis/cloud/speech/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const DefaultSampleRate = 48000

// AudioStreamPlayer audio stream player by aplay command
type AudioStreamPlayer struct {
	stopflag bool
	// client   *speech.Client
	stream speechpb.Speech_StreamingRecognizeClient
	result string
	ch     chan []byte
}

// NewAudioStreamPlayer new AudioStreamPlayer
func NewAudioStreamPlayer() *AudioStreamPlayer {
	return &AudioStreamPlayer{
		// client: client,
		ch: make(chan []byte, 10),
	}
}

// Kill kill command and goroutine
func (s *AudioStreamPlayer) Kill() {
	s.stopflag = true
	s.ch <- []byte{}
}

// Put put data
func (s *AudioStreamPlayer) Put(dat []byte) {
	s.ch <- dat
}

// Loop main loop
func (s *AudioStreamPlayer) Send(ctx context.Context) {
	// file, _ := os.Create("file/test.txt")
	for {
		d := <-s.ch
		if s.stopflag {
			break
		}
		if err := s.stream.Send(&speechpb.StreamingRecognizeRequest{
			StreamingRequest: &speechpb.StreamingRecognizeRequest_AudioContent{
				AudioContent: d,
			},
		}); err != nil {
			log.Printf("Could not send audio: %v\n", err)
			return
		}
		// log.Println("sended")
	}
	log.Println("player exiting")
	close(s.ch)
	s.ch = nil
	log.Println("player exit")
}

func (s *AudioStreamPlayer) ReadMessage(ws *websocket.Conn) {
	for {
		_, dat, err := ws.ReadMessage()

		if err != nil {
			log.Println("ws read error:", err)
			s.Kill()
			break
		}
		if s.stopflag {
			break
		}

		s.Put(dat)
	}
}

type conversationSchema struct {
	ID        string    `firestore:"-" json:"-"`
	Name      string    `firestore:"name" json:"name"`
	Text      string    `firestore:"text" json:"text"`
	CreatedAt time.Time `firestore:"-" json:"createdAt"`
}

type conversationSchemas []conversationSchema

func (css conversationSchemas) sort() conversationSchemas {
	sort.Slice(css, func(i, j int) bool {
		return css[i].CreatedAt.Before(css[j].CreatedAt)
	})

	return css
}

func (s *AudioStreamPlayer) Receive(ws *websocket.Conn, ctx context.Context, roomID string, name string) {
	colRef := fsClient.Collection("rooms").Doc(roomID).Collection("conversations")
	snapIter := colRef.Snapshots(ctx)
	defer snapIter.Stop()

	addedName := ""
	go func() {
		for {
			if s.stopflag {
				break
			}
			// ドキュメントが更新されたら受け取る
			snap, err := snapIter.Next()
			if err != nil {
				log.Println(fmt.Errorf("Failed to get snap: %v", err))
				return
			}
			docs, err := snap.Documents.GetAll()
			if err != nil {
				log.Println(fmt.Errorf("Failed to get all docs: %v", err))
				return
			}

			if len(docs) == 0 {
				continue
			}

			results := make(conversationSchemas, len(docs))
			for i, doc := range docs {
				con := conversationSchema{}
				doc.DataTo(&con)
				con.CreatedAt = doc.CreateTime
				results[i] = con
			}
			log.Println("conversationSchemas", results)

			// 会話をドキュメントの作成日時でソートし、フロントに返却
			css := results.sort()
			bytes, _ := json.Marshal(css)

			if err := ws.WriteMessage(2, bytes); err != nil {
				log.Println(fmt.Errorf("Could not transcript: %v", err))
				return
			}
			// 最後に追加されたドキュメントの発言者名を持っておく
			addedName = css[len(css)-1].Name
		}
	}()

	docID, err := newConversation(ctx, colRef, name)
	if err != nil {
		log.Println(fmt.Errorf("Failed to add doc: %v", err))
		return
	}

	updateChan := make(chan conversationSchema)
	go func() {
		for {
			if s.stopflag {
				break
			}
			// ドキュメントを更新
			c := <-updateChan
			if c.Text != "" {
				colRef.Doc(c.ID).Update(ctx, []firestore.Update{
					{Path: "text", Value: c.Text},
				})
			}
		}
	}()

	updated := time.Now()
	// 変換が確定した文字列を保持しておく
	text := ""
	for {
		resp, err := s.stream.Recv()
		if err == io.EOF {
			return
		}
		if err != nil {
			log.Println("Cannot stream results: ", err)
			return
		}
		if err := resp.Error; err != nil {
			if err.Code != 11 {
				log.Println("Could not recognize: ", err)
				return
			}
			log.Println(err)

			if s.stopflag {
				break
			}
			s.Restart(ctx)
		}
		// show result
		if len(resp.Results) == 0 {
			continue
		}
		result := resp.Results[0]
		if len(result.Alternatives) == 0 {
			continue
		}

		alt := result.Alternatives[0]
		fmt.Printf("Transcript: %s\n", alt.Transcript)

		r := alt.Transcript
		now := time.Now()
		if result.IsFinal {

			// 発言の切れ目が来たら句点を入れてみる
			if !strings.HasSuffix(r, "。") && !strings.HasSuffix(r, "、") {
				r += "。"
			}
			text = text + r

			updateChan <- conversationSchema{ID: docID, Text: text}
			updated = now

			// 別の人が発言していたら次のドキュメントに書き込む
			if addedName != name {
				docID, err = newConversation(ctx, colRef, name)
				if err != nil {
					log.Println(fmt.Errorf("Failed to add doc: %v", err))
					return
				}
				text = ""
			}

			if s.stopflag {
				break
			}
			continue
		}

		// firestoreの書き込み制限のためとりあえず1秒
		if now.After(updated.Add(time.Second)) {
			updateChan <- conversationSchema{ID: docID, Text: text + r}
			updated = now
		}

		if s.stopflag {
			break
		}
	}
}

func newConversation(ctx context.Context, colRef *firestore.CollectionRef, name string) (string, error) {

	con := conversationSchema{
		Name: name,
	}

	doc, _, err := colRef.Add(ctx, con)
	if err != nil {
		return "", fmt.Errorf("Failed to add doc: %v", err)
	}
	return doc.ID, nil
}

// func updateConversation(ctx context.Context, colRef *firestore.CollectionRef, docID string, textChan chan string) {

// 	for {
// 		time.Sleep(1 * time.Second)
// 		text := ""
// 		for {
// 			currentText, ok := <-textChan
// 			if !ok {
// 				break
// 			}
// 			text = currentText
// 		}

// 		colRef.Doc(docID).Update(ctx, []firestore.Update{
// 			{Path: "text", Value: text},
// 		})
// 	}
// }

// type Words struct {
// 	SpeakerTag int32
// 	Word       string
// }

func (s *AudioStreamPlayer) Restart(ctx context.Context) {

	stream, err := speechClient.StreamingRecognize(ctx)
	if err != nil {
		log.Println(err)
		return
	}

	// Send the initial configuration message.
	if err := stream.Send(&speechpb.StreamingRecognizeRequest{
		StreamingRequest: &speechpb.StreamingRecognizeRequest_StreamingConfig{
			StreamingConfig: &speechpb.StreamingRecognitionConfig{
				Config: &speechpb.RecognitionConfig{
					Encoding:        speechpb.RecognitionConfig_LINEAR16,
					SampleRateHertz: DefaultSampleRate,
					LanguageCode:    "ja-JP",
					// DiarizationConfig: &speechpb.SpeakerDiarizationConfig{
					// 	EnableSpeakerDiarization: true, // 話者を判別してくれる
					// 	MinSpeakerCount:          1,
					// 	// MaxSpeakerCount:          2,
					// },
					EnableAutomaticPunctuation: true, // 句読点を付けてくれる
					// Model:                      "video", // 複数人で話したときの精度が上がるけど料金も上がる（らしい）-> "ja-JP"は未対応だった…
				},
				// SingleUtterance: true, // 発話を一時停止したことを検出すると以降受信しなくなる
				InterimResults: true, // 分析途中の結果を返してくれる
			},
		},
	}); err != nil {
		log.Println(err)
		return
	}
	s.stream = stream
}

func speakerHandler(w http.ResponseWriter, r *http.Request) {

	var upgrader = websocket.Upgrader{}
	upgrader.CheckOrigin = func(r *http.Request) bool {
		return true
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Println("upgrade:", err)
		return
	}
	defer ws.Close()
	log.Printf("ws connect %p\n", ws)

	ctx := r.Context()

	// if !auth(r) {
	// 	log.Println("Not authorized")
	// 	if err := ws.WriteMessage(2, []byte("Not authorized")); err != nil {
	// 		log.Println(fmt.Errorf("Could not transcript: %v", err))
	// 		return
	// 	}
	// 	return
	// }

	if len(r.URL.Query()["id"]) == 0 {
		log.Printf("invalid parameter")
		if err := ws.WriteMessage(2, []byte("invalid parameter")); err != nil {
			log.Println(fmt.Errorf("Could not transcript: %v", err))
			return
		}
		return
	}
	roomID := r.URL.Query()["id"][0]

	if !existRoom(ctx, roomID) {
		if err := ws.WriteMessage(2, []byte("room not exist")); err != nil {
			log.Println(fmt.Errorf("Could not transcript: %v", err))
			return
		}
		return
	}

	if len(r.URL.Query()["name"]) == 0 {
		log.Printf("invalid parameter")
		if err := ws.WriteMessage(2, []byte("invalid parameter")); err != nil {
			log.Println(fmt.Errorf("Could not transcript: %v", err))
			return
		}
		return
	}
	name := r.URL.Query()["name"][0]

	player := NewAudioStreamPlayer()
	player.Restart(ctx)

	go player.Send(ctx)
	go player.ReadMessage(ws)
	player.Receive(ws, ctx, roomID, name)
}

var (
	fsClient     *firestore.Client
	speechClient *speech.Client
)

func main() {

	ctx := context.Background()

	var err error
	speechClient, err = speech.NewClient(ctx)
	if err != nil {
		log.Fatal(err)
	}

	projectID := os.Getenv("PROJECT_ID")

	fsClient, err = firestore.NewClient(ctx, projectID)
	if err != nil {
		log.Fatal(err)
	}
	// ctx, cancel := context.WithTimeout(ctx, 59*time.Second)
	// sig := make(chan os.Signal)

	// go func() {
	// 	<-sig
	// 	cancel()
	// }()

	// signal.Notify(sig, os.Interrupt)

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	addr := os.Getenv("HOST") + ":" + port

	log.Println("start server. addr:", addr)

	// http.HandleFunc("/", handle)
	http.Handle("/", http.FileServer(http.Dir("client/dist")))
	http.HandleFunc("/room", createRoomHandler)
	http.HandleFunc("/speaker", speakerHandler)
	http.ListenAndServe(addr, nil)
}

func handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	fmt.Fprint(w, "Hello world!")
}

// // 最低限の認証入れたけどIAPでできるから不要だった
// var (
// 	authUser     = os.Getenv("AUTH_USER")
// 	authPassword = os.Getenv("AUTH_PASSWORD")
// )

// func auth(r *http.Request) bool {

// 	if len(r.URL.Query()["username"]) == 0 {
// 		log.Printf("invalid parameter")
// 		return false
// 	}
// 	username := r.URL.Query()["username"][0]

// 	if len(r.URL.Query()["password"]) == 0 {
// 		log.Printf("invalid parameter")
// 		return false
// 	}
// 	password := r.URL.Query()["password"][0]

// 	if username != authUser || password != authPassword {
// 		log.Printf("Not authorized")
// 		return false
// 	}
// 	return true
// }

type room struct {
	RoomID string `json:"roomId"`
}

func createRoomHandler(w http.ResponseWriter, r *http.Request) {

	// if !auth(r) {
	// 	w.WriteHeader(http.StatusUnauthorized)
	// 	w.Write([]byte("Not authorized"))
	// 	return
	// }

	ctx := r.Context()

	if r.Method != "POST" {
		log.Println("method not allowed")
		w.WriteHeader(http.StatusMethodNotAllowed)
		w.Write([]byte("method not allowed"))
		return
	}
	doc, _, err := fsClient.Collection("rooms").Add(ctx, struct{}{})
	if err != nil {
		log.Println(err)
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal server error"))
		return
	}

	log.Printf("create room :%v\n", doc.ID)

	Render(ctx, w, room{
		RoomID: doc.ID,
	})
}

func existRoom(ctx context.Context, id string) bool {
	_, err := fsClient.Collection("rooms").Doc(id).Get(ctx)
	if status.Code(err) == codes.NotFound {
		log.Printf("room not exist")
		return false
	}
	if err != nil {
		log.Println(err)
		return false
	}
	return true
}

// Render .
func Render(ctx context.Context, w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	jsonBytes, err := json.Marshal(data)
	if err != nil {
		log.Println(err)
	}
	w.Write(jsonBytes)
}

// func sendToSpeech(ctx context.Context, stream speechpb.Speech_StreamingRecognizeClient, data []byte) {
// 	// defer func() {
// 	// 	if err := stream.CloseSend(); err != nil {
// 	// 		log.Printf("Could not close stream: %v\n", err)
// 	// 	}
// 	// }()

// 	// for {

// 	select {
// 	case <-ctx.Done():
// 		if err := ctx.Err(); err != nil {
// 			switch err {
// 			case context.Canceled:
// 				log.Println("Aborted.")
// 			case context.DeadlineExceeded:
// 				log.Println("Timeout.")
// 			}
// 		}
// 		return
// 	default:
// 		if err := stream.Send(&speechpb.StreamingRecognizeRequest{
// 			StreamingRequest: &speechpb.StreamingRecognizeRequest_AudioContent{
// 				AudioContent: data,
// 			},
// 		}); err != nil {
// 			log.Printf("Could not send audio: %v\n", err)
// 			return
// 		}
// 		// log.Println("sended")
// 	}
// 	// }
// }

// func receiveFromSpeech(stream speechpb.Speech_StreamingRecognizeClient, ws *websocket.Conn) error {
// 	type Words struct {
// 		SpeakerTag int32
// 		Word       string
// 	}
// 	for {
// 		resp, err := stream.Recv()
// 		if err == io.EOF {
// 			return nil
// 		}
// 		if err != nil {
// 			return fmt.Errorf("Cannot stream results: %v", err)
// 		}
// 		if err := resp.Error; err != nil {
// 			if err.Code != 11 {
// 				return fmt.Errorf("Could not recognize: %v", err)
// 			}
// 		}
// 		// show result
// 		if len(resp.Results) == 0 {
// 			continue
// 		}
// 		result := resp.Results[0]
// 		if len(result.Alternatives) == 0 {
// 			continue
// 		}
// 		words := make([]Words, 0)
// 		// fmt.Printf("result: %+v\n", result)
// 		fmt.Printf("result.ChannelTag: %v\n", result.ChannelTag)
// 		currentWords := Words{}
// 		fmt.Printf("IsFinal: %v\n", result.IsFinal)

// 		alt := result.Alternatives[0]
// 		// fmt.Printf("alt: %+v\n", alt)
// 		fmt.Printf("Transcript: %s\n", alt.Transcript)
// 		// fmt.Printf("words: %+v\n", alt.Words)

// 		if err := ws.WriteMessage(2, []byte(alt.Transcript)); err != nil {
// 			return fmt.Errorf("Could not transcript: %v", err)
// 		}

// 		if len(alt.Words) == 0 {
// 			continue
// 		}

// 		word := alt.Words[0]
// 		currentWords = Words{SpeakerTag: word.SpeakerTag, Word: strings.Split(word.Word, "|")[0]}
// 		for _, word := range alt.Words[1:] {
// 			// pp.Println(word)
// 			if word.StartTime.Nanos == 0 && word.StartTime.Seconds == 0 {
// 				break
// 			}
// 			if currentWords.SpeakerTag == word.SpeakerTag {
// 				currentWords.Word = currentWords.Word + strings.Split(word.Word, "|")[0]
// 				continue
// 			}
// 			words = append(words, currentWords)
// 			currentWords = Words{SpeakerTag: word.SpeakerTag, Word: strings.Split(word.Word, "|")[0]}
// 			// words[word.SpeakerTag] = append(words[word.SpeakerTag], word.Word)
// 		}

// 		// if err := ws.WriteMessage(2, []byte(alt.Transcript)); err != nil {
// 		// 	return fmt.Errorf("Could not transcript: %v", err)
// 		// }

// 		// words = append(words, currentWords)
// 		// fmt.Printf("Words: %+v\n", words)
// 		// for _, word := range words {
// 		// 	fmt.Printf("SpeakerTag: %v, Word: %v\n", word.SpeakerTag, word.Word)
// 		// }
// 		// bytes, _ := json.Marshal(words)
// 		// if err := ws.WriteMessage(2, bytes); err != nil {
// 		// 	return fmt.Errorf("Could not transcript: %v", err)
// 		// }
// 	}
// }

// func receiveFromSpeech(stream speechpb.Speech_StreamingRecognizeClient, ws *websocket.Conn) error {
// 	type Words struct {
// 		SpeakerTag int32
// 		Word       string
// 	}
// 	for {
// 		resp, err := stream.Recv()
// 		if err == io.EOF {
// 			return nil
// 		}
// 		if err != nil {
// 			return fmt.Errorf("Cannot stream results: %v", err)
// 		}
// 		if err := resp.Error; err != nil {
// 			return fmt.Errorf("Could not recognize: %v", err)
// 		}
// 		// show result
// 		for _, result := range resp.Results {
// 			words := make([]Words, 0)
// 			// fmt.Printf("result: %+v\n", result)
// 			fmt.Printf("result.ChannelTag: %v\n", result.ChannelTag)
// 			currentWords := Words{}
// 			fmt.Printf("IsFinal: %v\n", result.IsFinal)

// 			for _, alt := range result.Alternatives {
// 				// fmt.Printf("alt: %+v\n", alt)
// 				fmt.Printf("Transcript: %s\n", alt.Transcript)
// 				// fmt.Printf("words: %+v\n", alt.Words)

// 				if len(alt.Words) == 0 {
// 					break
// 				}
// 				word := alt.Words[0]
// 				currentWords = Words{SpeakerTag: word.SpeakerTag, Word: strings.Split(word.Word, "|")[0]}
// 				for _, word := range alt.Words[1:] {
// 					// pp.Println(word)
// 					if word.StartTime.Nanos == 0 && word.StartTime.Seconds == 0 {
// 						break
// 					}
// 					if currentWords.SpeakerTag == word.SpeakerTag {
// 						currentWords.Word = currentWords.Word + strings.Split(word.Word, "|")[0]
// 						continue
// 					}
// 					words = append(words, currentWords)
// 					currentWords = Words{SpeakerTag: word.SpeakerTag, Word: strings.Split(word.Word, "|")[0]}
// 					// words[word.SpeakerTag] = append(words[word.SpeakerTag], word.Word)
// 				}

// 				if err := ws.WriteMessage(2, []byte(alt.Transcript)); err != nil {
// 					return fmt.Errorf("Could not transcript: %v", err)
// 				}
// 			}

// 			// words = append(words, currentWords)
// 			// fmt.Printf("Words: %+v\n", words)
// 			// for _, word := range words {
// 			// 	fmt.Printf("SpeakerTag: %v, Word: %v\n", word.SpeakerTag, word.Word)
// 			// }
// 			// bytes, _ := json.Marshal(words)
// 			// if err := ws.WriteMessage(2, bytes); err != nil {
// 			// 	return fmt.Errorf("Could not transcript: %v", err)
// 			// }
// 		}
// 	}
// }

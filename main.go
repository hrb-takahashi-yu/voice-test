package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"

	speech "cloud.google.com/go/speech/apiv1"
	"github.com/gorilla/websocket"
	"golang.org/x/net/context"
	speechpb "google.golang.org/genproto/googleapis/cloud/speech/v1"
)

const DefaultSampleRate = 48000

// AudioStreamPlayer audio stream player by aplay command
type AudioStreamPlayer struct {
	stopflag bool
	client   *speech.Client
	stream   speechpb.Speech_StreamingRecognizeClient
	result   string
	ch       chan []byte
}

// NewAudioStreamPlayer new AudioStreamPlayer
func NewAudioStreamPlayer(client *speech.Client) *AudioStreamPlayer {
	return &AudioStreamPlayer{
		client: client,
		ch:     make(chan []byte, 10),
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
		log.Println("sended")
		// sendToSpeech(ctx, stream, d)
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

func (s *AudioStreamPlayer) Receive(ws *websocket.Conn, ctx context.Context) {
	for {
		resp, err := s.stream.Recv()
		if err == io.EOF {
			return
		}
		if err != nil {
			log.Fatal(fmt.Errorf("Cannot stream results: %v", err))
			return
		}
		if err := resp.Error; err != nil {
			if err.Code != 11 {
				log.Fatal(fmt.Errorf("Could not recognize: %v", err))
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

		r := s.result + alt.Transcript
		if result.IsFinal {
			if !strings.HasSuffix(r, "。") {
				r += "。"
			}
			r += "\n"
			s.result = r
			fmt.Printf("s.result: %s\n", s.result)
		}

		if s.stopflag {
			break
		}
		if err := ws.WriteMessage(2, []byte(r)); err != nil {
			log.Fatal(fmt.Errorf("Could not transcript: %v", err))
			return
		}
	}
}

func (s *AudioStreamPlayer) Restart(ctx context.Context) {

	stream, err := s.client.StreamingRecognize(ctx)
	if err != nil {
		log.Fatal(err)
	}

	// Send the initial configuration message.
	if err := stream.Send(&speechpb.StreamingRecognizeRequest{
		StreamingRequest: &speechpb.StreamingRecognizeRequest_StreamingConfig{
			StreamingConfig: &speechpb.StreamingRecognitionConfig{
				Config: &speechpb.RecognitionConfig{
					Encoding:        speechpb.RecognitionConfig_LINEAR16,
					SampleRateHertz: DefaultSampleRate,
					LanguageCode:    "ja-JP",
					DiarizationConfig: &speechpb.SpeakerDiarizationConfig{
						EnableSpeakerDiarization: true, // 話者を判別してくれる
						MinSpeakerCount:          2,
						MaxSpeakerCount:          2,
					},
					EnableAutomaticPunctuation: true, // 句読点を付けてくれる
					// Model:                      "video", // 複数人で話したときの精度が上がるけど料金も上がる（らしい）-> "ja-JP"は未対応だった…
				},
				// SingleUtterance: true, // 発話を一時停止したことを検出すると以降受信しなくなる
				InterimResults: true, // 分析途中の結果を返してくれる
			},
		},
	}); err != nil {
		log.Fatal(err)
	}
	s.stream = stream
}

// SpeakerHandler speaker server handler
func genSpeakerHandler(ctx context.Context, client *speech.Client) func(w http.ResponseWriter, r *http.Request) {
	res := func(w http.ResponseWriter, r *http.Request) {
		var upgrader = websocket.Upgrader{}
		upgrader.CheckOrigin = func(r *http.Request) bool {
			return true
		}

		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			log.Print("upgrade:", err)
			return
		}
		defer ws.Close()
		log.Printf("ws connect %p\n", ws)

		// stream, err := client.StreamingRecognize(ctx)
		// if err != nil {
		// 	log.Fatal(err)
		// }

		// // Send the initial configuration message.
		// if err := stream.Send(&speechpb.StreamingRecognizeRequest{
		// 	StreamingRequest: &speechpb.StreamingRecognizeRequest_StreamingConfig{
		// 		StreamingConfig: &speechpb.StreamingRecognitionConfig{
		// 			Config: &speechpb.RecognitionConfig{
		// 				Encoding:        speechpb.RecognitionConfig_LINEAR16,
		// 				SampleRateHertz: DefaultSampleRate,
		// 				LanguageCode:    "ja-JP",
		// 				DiarizationConfig: &speechpb.SpeakerDiarizationConfig{
		// 					EnableSpeakerDiarization: true,
		// 					MinSpeakerCount:          2,
		// 					MaxSpeakerCount:          2,
		// 				},
		// 				EnableAutomaticPunctuation: true,
		// 				// Model:                      "video",
		// 			},
		// 			// SingleUtterance: true,
		// 			InterimResults: true,
		// 		},
		// 	},
		// }); err != nil {
		// 	log.Fatal(err)
		// }
		player := NewAudioStreamPlayer(client)
		player.Restart(ctx)

		go player.Send(ctx)
		go player.ReadMessage(ws)
		player.Receive(ws, ctx)
		// if err := receiveFromSpeech(stream, ws); err != nil {
		// 	log.Println(err)
		// 	player.Kill()
		// 	return
		// }
	}
	return res
}

func main() {

	ctx := context.Background()

	// [START speech_streaming_mic_recognize]
	client, err := speech.NewClient(ctx)
	// client, err := speech.NewClient(ctx)
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

	speakerHandler := genSpeakerHandler(ctx, client)

	addr := "0.0.0.0:3004"

	log.Println("start server. add:", addr)

	http.HandleFunc("/speaker", speakerHandler)
	http.ListenAndServe(addr, nil)
}

func sendToSpeech(ctx context.Context, stream speechpb.Speech_StreamingRecognizeClient, data []byte) {
	// defer func() {
	// 	if err := stream.CloseSend(); err != nil {
	// 		log.Printf("Could not close stream: %v\n", err)
	// 	}
	// }()

	// for {

	select {
	case <-ctx.Done():
		if err := ctx.Err(); err != nil {
			switch err {
			case context.Canceled:
				log.Println("Aborted.")
			case context.DeadlineExceeded:
				log.Println("Timeout.")
			}
		}
		return
	default:
		if err := stream.Send(&speechpb.StreamingRecognizeRequest{
			StreamingRequest: &speechpb.StreamingRecognizeRequest_AudioContent{
				AudioContent: data,
			},
		}); err != nil {
			log.Printf("Could not send audio: %v\n", err)
			return
		}
		// log.Println("sended")
	}
	// }
}

func receiveFromSpeech(stream speechpb.Speech_StreamingRecognizeClient, ws *websocket.Conn) error {
	type Words struct {
		SpeakerTag int32
		Word       string
	}
	for {
		resp, err := stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("Cannot stream results: %v", err)
		}
		if err := resp.Error; err != nil {
			if err.Code != 11 {
				return fmt.Errorf("Could not recognize: %v", err)
			}
		}
		// show result
		if len(resp.Results) == 0 {
			continue
		}
		result := resp.Results[0]
		if len(result.Alternatives) == 0 {
			continue
		}
		words := make([]Words, 0)
		// fmt.Printf("result: %+v\n", result)
		fmt.Printf("result.ChannelTag: %v\n", result.ChannelTag)
		currentWords := Words{}
		fmt.Printf("IsFinal: %v\n", result.IsFinal)

		alt := result.Alternatives[0]
		// fmt.Printf("alt: %+v\n", alt)
		fmt.Printf("Transcript: %s\n", alt.Transcript)
		// fmt.Printf("words: %+v\n", alt.Words)

		if err := ws.WriteMessage(2, []byte(alt.Transcript)); err != nil {
			return fmt.Errorf("Could not transcript: %v", err)
		}

		if len(alt.Words) == 0 {
			continue
		}

		word := alt.Words[0]
		currentWords = Words{SpeakerTag: word.SpeakerTag, Word: strings.Split(word.Word, "|")[0]}
		for _, word := range alt.Words[1:] {
			// pp.Println(word)
			if word.StartTime.Nanos == 0 && word.StartTime.Seconds == 0 {
				break
			}
			if currentWords.SpeakerTag == word.SpeakerTag {
				currentWords.Word = currentWords.Word + strings.Split(word.Word, "|")[0]
				continue
			}
			words = append(words, currentWords)
			currentWords = Words{SpeakerTag: word.SpeakerTag, Word: strings.Split(word.Word, "|")[0]}
			// words[word.SpeakerTag] = append(words[word.SpeakerTag], word.Word)
		}

		// if err := ws.WriteMessage(2, []byte(alt.Transcript)); err != nil {
		// 	return fmt.Errorf("Could not transcript: %v", err)
		// }

		// words = append(words, currentWords)
		// fmt.Printf("Words: %+v\n", words)
		// for _, word := range words {
		// 	fmt.Printf("SpeakerTag: %v, Word: %v\n", word.SpeakerTag, word.Word)
		// }
		// bytes, _ := json.Marshal(words)
		// if err := ws.WriteMessage(2, bytes); err != nil {
		// 	return fmt.Errorf("Could not transcript: %v", err)
		// }
	}
}

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

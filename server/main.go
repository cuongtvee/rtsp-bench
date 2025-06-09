package main

import (
	"encoding/json"
	"fmt"
	"github.com/bluenviron/gortsplib/v4"
	"github.com/bluenviron/gortsplib/v4/pkg/base"
	"github.com/bluenviron/gortsplib/v4/pkg/format"
	"github.com/bluenviron/gortsplib/v4/pkg/format/rtph264"
	"github.com/bluenviron/mediacommon/v2/pkg/codecs/h264"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"io"
	"log"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/deepch/vdk/av"
	"github.com/deepch/vdk/codec/h264parser"
	"github.com/deepch/vdk/format/rtsp"
	"github.com/pion/webrtc/v3/pkg/media"
)

var (
	outboundVideoTrack  *webrtc.TrackLocalStaticSample
	peerConnectionCount int64
)

// HTTP Handler that accepts an Offer and returns an Answer
// adds outboundVideoTrack to PeerConnection
func doSignaling(w http.ResponseWriter, r *http.Request) {
	peerConnection, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		panic(err)
	}

	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		if connectionState == webrtc.ICEConnectionStateDisconnected {
			atomic.AddInt64(&peerConnectionCount, -1)
			if err := peerConnection.Close(); err != nil {
				panic(err)
			}
		} else if connectionState == webrtc.ICEConnectionStateConnected {
			atomic.AddInt64(&peerConnectionCount, 1)
		}
	})

	if _, err = peerConnection.AddTrack(outboundVideoTrack); err != nil {
		panic(err)
	}

	var offer webrtc.SessionDescription
	if err = json.NewDecoder(r.Body).Decode(&offer); err != nil {
		panic(err)
	}

	if err = peerConnection.SetRemoteDescription(offer); err != nil {
		panic(err)
	}

	gatherCompletePromise := webrtc.GatheringCompletePromise(peerConnection)

	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		panic(err)
	} else if err = peerConnection.SetLocalDescription(answer); err != nil {
		panic(err)
	}

	<-gatherCompletePromise

	response, err := json.Marshal(*peerConnection.LocalDescription())
	if err != nil {
		panic(err)
	}

	w.Header().Set("Content-Type", "application/json")
	if _, err := w.Write(response); err != nil {
		panic(err)
	}
}

func main() {
	var err error
	outboundVideoTrack, err = webrtc.NewTrackLocalStaticSample(webrtc.RTPCodecCapability{
		MimeType: "video/h264",
	}, "pion-rtsp", "pion-rtsp")
	if err != nil {
		panic(err)
	}

	go rtspConsumer1()

	http.Handle("/", http.FileServer(http.Dir("./static")))
	http.HandleFunc("/doSignaling", doSignaling)

	fmt.Println("Open http://localhost:8080 to access this demo")
	panic(http.ListenAndServe(":8080", nil))
}

// The RTSP URL that will be streamed
const rtspURL = "rtsp://admin:admin@192.168.1.5:554/live"

// Connect to an RTSP URL and pull media.
// Convert H264 to Annex-B, then write to outboundVideoTrack which sends to all PeerConnections
func rtspConsumer() {
	annexbNALUStartCode := func() []byte { return []byte{0x00, 0x00, 0x00, 0x01} }

	for {
		session, err := rtsp.Dial(rtspURL)
		if err != nil {
			panic(err)
		}
		session.RtpKeepAliveTimeout = 10 * time.Second

		codecs, err := session.Streams()
		if err != nil {
			panic(err)
		}
		for i, t := range codecs {
			log.Println("Stream", i, "is of type", t.Type().String())
		}
		if codecs[0].Type() != av.H264 {
			panic("RTSP feed must begin with a H264 codec")
		}
		if len(codecs) != 1 {
			log.Println("Ignoring all but the first stream.")
		}

		var previousTime time.Duration
		for {
			pkt, err := session.ReadPacket()
			if err != nil {
				break
			}

			if pkt.Idx != 0 {
				//audio or other stream, skip it
				continue
			}

			pkt.Data = pkt.Data[4:]

			// For every key-frame pre-pend the SPS and PPS
			if pkt.IsKeyFrame {
				pkt.Data = append(annexbNALUStartCode(), pkt.Data...)
				pkt.Data = append(codecs[0].(h264parser.CodecData).PPS(), pkt.Data...)
				pkt.Data = append(annexbNALUStartCode(), pkt.Data...)
				pkt.Data = append(codecs[0].(h264parser.CodecData).SPS(), pkt.Data...)
				pkt.Data = append(annexbNALUStartCode(), pkt.Data...)
			}

			bufferDuration := pkt.Time - previousTime
			previousTime = pkt.Time
			if err = outboundVideoTrack.WriteSample(media.Sample{Data: pkt.Data, Duration: bufferDuration}); err != nil && err != io.ErrClosedPipe {
				panic(err)
			}
		}

		if err = session.Close(); err != nil {
			log.Println("session Close error", err)
		}

		time.Sleep(5 * time.Second)
	}
}

var annexBStartCode = []byte{0x00, 0x00, 0x00, 0x01}

func rtspConsumer1() {
	// Define the RTSP source
	rtspURL := "rtsp://admin:admin@192.168.1.5:554/live"

	for {
		trs := gortsplib.TransportTCP
		client := &gortsplib.Client{
			Transport: &trs,
		}

		u, err := base.ParseURL(rtspURL)
		if err != nil {
			log.Println("Failed to parse RTSP URL:", err)
			time.Sleep(5 * time.Second)
			continue
		}

		if err := client.Start(u.Scheme, u.Host); err != nil {
			log.Println("Failed to connect to RTSP server:", err)
			time.Sleep(5 * time.Second)
			continue
		}

		desc, _, err := client.Describe(u)
		if err != nil {
			log.Println("Failed to describe RTSP stream:", err)
			client.Close()
			time.Sleep(5 * time.Second)
			continue
		}

		var forma *format.H264
		medi := desc.FindFormat(&forma)
		if medi == nil {
			log.Println("No H264 media found in stream")
			client.Close()
			time.Sleep(5 * time.Second)
			continue
		}

		rtpDec, err := forma.CreateDecoder()
		if err != nil {
			log.Println("Failed to create RTP decoder:", err)
			client.Close()
			time.Sleep(5 * time.Second)
			continue
		}

		// Store SPS and PPS
		sps := forma.SPS
		pps := forma.PPS

		if _, err := client.Setup(desc.BaseURL, medi, 0, 0); err != nil {
			log.Println("Setup failed:", err)
			client.Close()
			time.Sleep(5 * time.Second)
			continue
		}

		var lastTime time.Time
		firstRandomAccess := false
		client.OnPacketRTP(medi, forma, func(pkt *rtp.Packet) {
			au, err := rtpDec.Decode(pkt)
			if err != nil {
				if err != rtph264.ErrNonStartingPacketAndNoPrevious &&
					err != rtph264.ErrMorePacketsNeeded {
					log.Println("Decode error:", err)
				}
				return
			}

			// wait for a random access unit
			if !firstRandomAccess && !h264.IsRandomAccess(au) {
				log.Printf("waiting for a random access unit")
				return
			}
			firstRandomAccess = true

			// Convert to Annex-B format
			nalus := au
			var annexb []byte
			if sps != nil && pps != nil {
				annexb = append(annexb, annexBStartCode...)
				annexb = append(annexb, sps...)
				annexb = append(annexb, annexBStartCode...)
				annexb = append(annexb, pps...)
			}
			for _, nalu := range nalus {
				annexb = append(annexb, annexBStartCode...)
				annexb = append(annexb, nalu...)
			}

			// Write to WebRTC track
			now := time.Now()
			duration := now.Sub(lastTime)
			lastTime = now

			err = outboundVideoTrack.WriteSample(media.Sample{
				Data:     annexb,
				Duration: duration,
			})
			if err != nil {
				log.Println("WriteSample error:", err)
			}
		})

		_, err = client.Play(nil)
		if err != nil {
			log.Println("Play failed:", err)
			client.Close()
			time.Sleep(5 * time.Second)
			continue
		}

		log.Println("RTSP client started")
		err = client.Wait()
		log.Println("RTSP client ended:", err)

		time.Sleep(5 * time.Second)
	}
}

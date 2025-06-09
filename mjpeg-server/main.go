package main

import (
	"bytes"
	"fmt"
	"github.com/bluenviron/gortsplib/v4/pkg/description"
	"image"
	"image/color"
	"image/jpeg"
	"log"
	"math/rand"
	"time"

	"github.com/bluenviron/gortsplib/v4/pkg/format"

	"github.com/bluenviron/gortsplib/v4"
)

func generateJPEG(width, height int) []byte {
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.Set(x, y, color.RGBA{
				R: uint8(rand.Intn(256)),
				G: uint8(rand.Intn(256)),
				B: uint8(rand.Intn(256)),
				A: 255,
			})
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
	return buf.Bytes()
}

func multiplyAndDivide(v, m, d int64) int64 {
	secs := v / d
	dec := v % d
	return (secs*m + dec*m/d)
}

func randUint32() (uint32, error) {
	var b [4]byte
	_, err := rand.Read(b[:])
	if err != nil {
		return 0, err
	}
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3]), nil
}

func main() {
	s := &server{}
	s.initialize()
	// create a RTSP description that contains a H264 format
	forma := &format.MJPEG{}
	desc := &description.Session{
		Medias: []*description.Media{{
			Type:    description.MediaTypeVideo,
			Formats: []format.Format{forma},
		}},
	}
	s.stream = &gortsplib.ServerStream{
		Server: s.server,
		Desc:   desc,
	}
	s.server.Start()
	err := s.stream.Initialize()
	if err != nil {
		fmt.Println(err.Error())
		return
	}
	defer s.stream.Close()

	// setup JPEG -> RTP encoder
	rtpEnc, err := forma.CreateEncoder()
	if err != nil {
		panic(err)
	}
	go func() {
		start := time.Now()
		randomStart, err := randUint32()
		if err != nil {
			panic(err)
		}
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		for range ticker.C {
			// get current timestamp
			pts := multiplyAndDivide(int64(time.Since(start)), int64(forma.ClockRate()), int64(time.Second))

			// create a dummy image
			img := createDummyImage()

			// encode the image with JPEG
			var buf bytes.Buffer
			err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80})
			if err != nil {
				panic(err)
			}

			// generate RTP packets from the JPEG image
			pkts, err := rtpEnc.Encode(buf.Bytes())
			if err != nil {
				panic(err)
			}

			log.Printf("writing RTP packets with PTS=%d, jpeg size=%d, pkt count=%d", pts, buf.Len(), len(pkts))

			// write RTP packets to the server
			for _, pkt := range pkts {
				pkt.Timestamp += uint32(int64(randomStart) + pts)

				err = s.stream.WritePacketRTP(desc.Medias[0], pkt)
				if err != nil {
					panic(err)
				}
			}
		}
	}()
	log.Printf("server is ready on %s", s.server.RTSPAddress)

	s.server.Wait()
}

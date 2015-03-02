package stampclient

import (
	"crypto/rand"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/Sirupsen/logrus"

	"github.com/dedis/prifi/coco/coconet"
	"github.com/dedis/prifi/coco/hashid"
	"github.com/dedis/prifi/coco/stamp"
	"github.com/dedis/prifi/coco/test/logutils"
)

func genRandomMessages(n int) [][]byte {
	msgs := make([][]byte, n)
	for i := range msgs {
		msgs[i] = make([]byte, hashid.Size)
		_, err := rand.Read(msgs[i])
		if err != nil {
			log.Fatal("failed to generate random commit:", err)
		}
	}
	return msgs
}

func removeTrailingZeroes(a []int64) []int64 {
	i := len(a) - 1
	for ; i >= 0; i-- {
		if a[i] != 0 {
			break
		}
	}
	return a[:i+1]
}

func AggregateStats(buck, roundsAfter []int64) string {
	log.WithFields(log.Fields{
		"file":        logutils.File(),
		"type":        "client_msg_stats",
		"buck":        removeTrailingZeroes(buck),
		"roundsAfter": removeTrailingZeroes(roundsAfter),
	}).Info("")
	return "Client Finished Aggregating Statistics"
}

func streamMessgs(c *stamp.Client, servers []string, rate int) {
	log.Println("STREAMING: GIVEN RATE")
	// buck[i] = # of timestamp responses received in second i
	buck := make([]int64, MAX_N_SECONDS)
	// roundsAfter[i] = # of timestamp requests that were processed i rounds late
	roundsAfter := make([]int64, MAX_N_ROUNDS)
	ticker := time.Tick(time.Duration(rate) * time.Millisecond)
	msg := genRandomMessages(1)[0]
	i := 0
	nServers := len(servers)
	var tFirst time.Time

retry:
	err := c.TimeStamp(msg, servers[0])
	if err == io.EOF {
		log.Fatal(AggregateStats(buck, roundsAfter))
	} else if err != nil {
		time.Sleep(500 * time.Millsecond)
		goto retry
	}

	tFirst := time.Now()

	// every tick send a time stamp request to every server specified
	for _ = range ticker {
		go func(msg []byte, s string) {
			t0 := time.Now()
			err := c.TimeStamp(msg, s)
			t := time.Since(t0)

			if err == io.EOF {
				log.Fatal(AggregateStats(buck, roundsAfter))
			} else if err != nil {
				// ignore errors
				return
			}
			log.Println("successfully timestamped item")

			// TODO: we might want to subtract a buffer from secToTimeStamp
			// to account for computation time
			secToTimeStamp := t.Seconds()
			secSinceFirst := time.Since(tFirst).Seconds()
			atomic.AddInt64(&buck[int(secSinceFirst)], 1)
			index := int(secToTimeStamp) / int(stamp.ROUND_TIME/time.Second)
			atomic.AddInt64(&roundsAfter[index], 1)

		}(msg, servers[i])

		i = (i + 1) % nServers
	}

}

var MAX_N_SECONDS int = 1 * 60 * 60 // 1 hours' worth of seconds
var MAX_N_ROUNDS int = MAX_N_SECONDS / int(stamp.ROUND_TIME/time.Second)

func Run(server string, nmsgs int, name string, rate int, debug bool) {
	c := stamp.NewClient(name)
	msgs := genRandomMessages(nmsgs + 20)
	servers := strings.Split(server, ",")

	// log.Println("connecting to servers:", servers)
	for _, s := range servers {
		h, p, err := net.SplitHostPort(s)
		if err != nil {
			log.Fatal("improperly formatted host")
		}
		pn, _ := strconv.Atoi(p)
		c.AddServer(s, coconet.NewTCPConn(net.JoinHostPort(h, strconv.Itoa(pn+1))))
	}

	// if rate specified send out one message every rate milliseconds
	if rate > 0 {
		// Stream time stamp requests
		streamMessgs(c, servers, rate)
		return
	}

	// rounds based messaging
	r := 0
	s := 0

	// log.Println("timeclient using rounds")
	log.Fatal("ROUNDS BASED RATE LIMITING DEPRECATED")
	for {
		//start := time.Now()
		var wg sync.WaitGroup
		for i := 0; i < nmsgs; i++ {
			wg.Add(1)
			go func(i, s int) {
				defer wg.Done()
				err := c.TimeStamp(msgs[i], servers[s])
				if err == io.EOF {
					log.WithFields(log.Fields{
						"file":        logutils.File(),
						"type":        "client_msg_stats",
						"buck":        make([]int64, 0),
						"roundsAfter": make([]int64, 0),
					}).Info("")

					log.Fatal("EOF: terminating time client")
				}
			}(i, s)
			s = (s + 1) % len(servers)
		}
		wg.Wait()
		//elapsed := time.Since(start)
		log.Println("client done with round")
		//log.WithFields(log.Fields{
		//"file":  logutils.File(),
		//"type":  "client_round",
		//"round": r,
		//"time":  elapsed,
		//}).Info("client round")
		r++
	}
}

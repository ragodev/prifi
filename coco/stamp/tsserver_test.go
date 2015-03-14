package stamp_test

import (
	"fmt"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	log "github.com/Sirupsen/logrus"

	"github.com/dedis/prifi/coco"
	"github.com/dedis/prifi/coco/coconet"
	"github.com/dedis/prifi/coco/sign"
	"github.com/dedis/prifi/coco/stamp"
	"github.com/dedis/prifi/coco/test/oldconfig"
)

// TODO: messages should be sent hashed eventually

// func init() {
// 	log.SetFlags(log.Lshortfile)
// 	//log.SetOutput(ioutil.Discard)
// }

// Configuration file data/exconf.json
//       0
//      / \
//     1   4
//    / \   \
//   2   3   5
func init() {
	coco.DEBUG = true
}

func TestTSSIntegrationHealthy(t *testing.T) {
	if err := runTSSIntegration(4, 4, 0); err != nil {
		t.Fatal(err)
	}
}

func TestTSSIntegrationFaulty(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping faulty test in short mode.")
	}

	// not mixing view changes with faults
	aux := atomic.LoadInt64(&sign.RoundsPerView)
	atomic.StoreInt64(&sign.RoundsPerView, 100)

	faultyNodes := make([]int, 0)
	faultyNodes = append(faultyNodes, 2, 5)
	if err := runTSSIntegration(4, 4, 20, faultyNodes...); err != nil {
		t.Fatal(err)
	}

	atomic.StoreInt64(&sign.RoundsPerView, aux)
}

// with 8 rounds and 1 round per view we see 7 successful view Changes, and 8 views
func TestTSSViewChange1(t *testing.T) {
	aux := atomic.LoadInt64(&sign.RoundsPerView)
	atomic.StoreInt64(&sign.RoundsPerView, 1)
	nRounds := 3

	if err := runTSSIntegration(1, nRounds, 0); err != nil {
		t.Fatal(err)
	}

	atomic.StoreInt64(&sign.RoundsPerView, aux)
}

// with 8 rounds and 3 rounds per view we see 2 successful view Changes, and 3 views
func TestTSSViewChange2(t *testing.T) {
	aux := atomic.LoadInt64(&sign.RoundsPerView)
	atomic.StoreInt64(&sign.RoundsPerView, 3)
	nRounds := 8

	if err := runTSSIntegration(1, nRounds, 0); err != nil {
		t.Fatal(err)
	}

	atomic.StoreInt64(&sign.RoundsPerView, aux)
}

// # Messages per round, # rounds, failure rate[0..100], list of faulty nodes
func runTSSIntegration(nMessages, nRounds, failureRate int, faultyNodes ...int) error {
	//stamp.ROUND_TIME = 1 * time.Second
	var hostConfig *oldconfig.HostConfig
	var err error

	// load config with faulty or healthy hosts
	opts := oldconfig.ConfigOptions{}
	if len(faultyNodes) > 0 {
		opts.Faulty = true
	}
	hostConfig, err = oldconfig.LoadConfig("../test/data/exconf.json", opts)
	if err != nil {
		return err
	}

	// set FailureRates
	if len(faultyNodes) > 0 {
		for i := range hostConfig.SNodes {
			hostConfig.SNodes[i].FailureRate = failureRate
		}
	}

	err = hostConfig.Run(sign.MerkleTree)
	if err != nil {
		return err
	}

	// Connect all TSServers to their clients, except for root TSServer
	ncps := 3 // # clients per TSServer
	stampers := make([]*stamp.Server, len(hostConfig.SNodes))
	for i := range stampers {
		stampers[i] = stamp.NewServer(hostConfig.SNodes[i])
		// defer func() {
		// 	log.Infoln("CLOSING STAMPER**********")
		// 	hostConfig.SNodes[i].Close()
		// }()
	}

	clientsLists := make([][]*stamp.Client, len(hostConfig.SNodes[1:]))
	for i, s := range stampers[1:] {
		clientsLists[i] = createClientsForTSServer(ncps, s, hostConfig.Dir, 0+i+ncps)
	}

	for i, s := range stampers[1:] {
		go s.Run("regular", nRounds)
		go s.ListenToClients()
		go func(clients []*stamp.Client, nRounds int, nMessages int, s *stamp.Server) {
			log.Println("clients Talk")
			time.Sleep(1 * time.Second)
			clientsTalk(clients, nRounds, nMessages, s)
			log.Println("Clients done Talking")
		}(clientsLists[i], nRounds, nMessages, s)

	}

	log.Println("RUNNING ROOT")
	stampers[0].Run("root", nRounds)
	log.Println("Done running root")
	// After clients receive messages back we need a better way
	// of waiting to make sure servers check ElGamal sigs
	// time.Sleep(1 * time.Second)
	log.Println("DONE with test")
	return nil
}

func TestGoConnTimestampFromConfig(t *testing.T) {
	oldconfig.StartConfigPort += 2010
	nMessages := 1
	nClients := 1
	nRounds := 1

	hc, err := oldconfig.LoadConfig("../test/data/exconf.json")
	if err != nil {
		t.Fatal(err)
	}
	err = hc.Run(sign.MerkleTree)
	if err != nil {
		t.Fatal(err)
	}

	stampers, clients, err := hc.RunTimestamper(nClients)
	if err != nil {
		log.Fatal(err)
	}

	for _, s := range stampers[1:] {
		go s.Run("regular", nRounds)
		go s.ListenToClients()
	}
	go stampers[0].Run("root", nRounds)
	go stampers[0].ListenToClients()
	log.Println("About to start sending client messages")

	time.Sleep(1 * time.Second)
	for r := 0; r < nRounds; r++ {
		var wg sync.WaitGroup
		for _, c := range clients {
			for i := 0; i < nMessages; i++ {
				messg := []byte("messg:" + strconv.Itoa(r) + "." + strconv.Itoa(i))
				wg.Add(1)
				go func(c *stamp.Client, messg []byte, i int) {
					defer wg.Done()
					server := "NO VALID SERVER"
					c.Mux.Lock()
					for k := range c.Servers {
						server = k
						break
					}
					c.Mux.Unlock()
					c.TimeStamp(messg, server)
				}(c, messg, r)
			}
		}
		// wait between rounds
		wg.Wait()
		fmt.Println("done with round:", r, nRounds)
	}

	// give it some time before closing the connections
	// so that no essential messages are denied passing through the network
	time.Sleep(5 * time.Second)
	for _, h := range hc.SNodes {
		h.Close()
	}
	for _, c := range clients {
		c.Close()
	}
}

func TestTCPTimestampFromConfigViewChange(t *testing.T) {
	if err := runTCPTimestampFromConfig(0); err != nil {
		t.Fatal(err)
	}
}

func TestTCPTimestampFromConfigHealthy(t *testing.T) {
	if err := runTCPTimestampFromConfig(0); err != nil {
		t.Fatal(err)
	}
}

func TestTCPTimestampFromConfigFaulty(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping faulty test in short mode.")
	}

	// not mixing view changes with faults
	aux := atomic.LoadInt64(&sign.RoundsPerView)
	atomic.StoreInt64(&sign.RoundsPerView, 100)

	faultyNodes := make([]int, 0)
	faultyNodes = append(faultyNodes, 2, 5)
	if err := runTCPTimestampFromConfig(20, faultyNodes...); err != nil {
		t.Fatal(err)
	}

	atomic.StoreInt64(&sign.RoundsPerView, aux)
}

func runTCPTimestampFromConfig(failureRate int, faultyNodes ...int) error {
	var hc *oldconfig.HostConfig
	var err error
	oldconfig.StartConfigPort += 2010
	nMessages := 1
	nClients := 1
	nRounds := 4

	// load config with faulty or healthy hosts
	if len(faultyNodes) > 0 {
		hc, err = oldconfig.LoadConfig("../test/data/extcpconf.json", oldconfig.ConfigOptions{ConnType: "tcp", GenHosts: true, Faulty: true})
	} else {
		hc, err = oldconfig.LoadConfig("../test/data/extcpconf.json", oldconfig.ConfigOptions{ConnType: "tcp", GenHosts: true})
	}
	if err != nil {
		fmt.Println("here")
		return err
	}

	// set FailureRates
	if len(faultyNodes) > 0 {
		for i := range hc.SNodes {
			hc.SNodes[i].FailureRate = failureRate
		}
	}

	err = hc.Run(sign.MerkleTree)
	if err != nil {
		return err
	}

	stampers, clients, err := hc.RunTimestamper(nClients)
	if err != nil {
		return err
	}

	for _, s := range stampers[1:] {
		go s.Run("regular", nRounds)
	}
	go stampers[0].Run("root", nRounds)
	log.Println("About to start sending client messages")

	for r := 1; r <= nRounds; r++ {
		var wg sync.WaitGroup
		for _, c := range clients {
			for i := 0; i < nMessages; i++ {
				messg := []byte("messg:" + strconv.Itoa(r) + "." + strconv.Itoa(i))
				wg.Add(1)

				// CLIENT SENDING
				go func(c *stamp.Client, messg []byte, i int) {
					defer wg.Done()
					server := "NO VALID SERVER"

				retry:
					c.Mux.Lock()
					for k := range c.Servers {
						server = k
						break
					}
					c.Mux.Unlock()
					log.Infoln("timestamping")
					err := c.TimeStamp(messg, server)
					if err == stamp.ErrClientToTSTimeout {
						log.Errorln(err)
						return
					}
					if err != nil {
						time.Sleep(1 * time.Second)
						fmt.Println("retyring because err:", err)
						goto retry
					}
					log.Infoln("timestamped")
				}(c, messg, r)

			}
		}
		// wait between rounds
		wg.Wait()
		log.Println("done with round:", r, " of ", nRounds)
	}

	// give it some time before closing the connections
	// so that no essential messages are denied passing through the network
	time.Sleep(10 * time.Second)
	for _, h := range hc.SNodes {
		h.Close()
	}
	for _, c := range clients {
		c.Close()
	}
	return nil
}

// Create nClients for the TSServer, with first client associated with number fClient
func createClientsForTSServer(nClients int, s *stamp.Server, dir *coconet.GoDirectory, fClient int) []*stamp.Client {
	clients := make([]*stamp.Client, 0, nClients)
	for i := 0; i < nClients; i++ {
		clients = append(clients, stamp.NewClient("client"+strconv.Itoa(fClient+i)))

		// intialize TSServer conn to client
		ngc, err := coconet.NewGoConn(dir, s.Name(), clients[i].Name())
		if err != nil {
			panic(err)
		}
		s.Clients[clients[i].Name()] = ngc

		// intialize client connection to sn
		ngc, err = coconet.NewGoConn(dir, clients[i].Name(), s.Name())
		if err != nil {
			panic(err)
		}
		clients[i].AddServer(s.Name(), ngc)
	}

	return clients
}

func clientsTalk(clients []*stamp.Client, nRounds, nMessages int, s *stamp.Server) {
	// have client send messages
	for r := 0; r < nRounds; r++ {
		var wg sync.WaitGroup
		for _, client := range clients {
			for i := 0; i < nMessages; i++ {
				messg := []byte("messg" + strconv.Itoa(r) + strconv.Itoa(i))
				wg.Add(1)
				go func(client *stamp.Client, messg []byte, s *stamp.Server, i int) {
					defer wg.Done()
					client.TimeStamp(messg, s.Name())
				}(client, messg, s, r)
			}
		}
		// wait between rounds
		wg.Wait()
	}
}

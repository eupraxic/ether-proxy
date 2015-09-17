package proxy

import (
	"../util"
	"log"
	"math/big"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/ethereum/ethash"
	"github.com/ethereum/go-ethereum/common"
)

var hasher = ethash.New()

type Miner struct {
	sync.RWMutex
	Id            string
	IP            string
	startTime     int64
	lastBeat      int64
	validShares   uint64
	invalidShares uint64
	invalidBlocks uint64
	validBlocks   uint64
	shares        map[int64]int64
}

func NewMiner(id, ip string) *Miner {
	miner := &Miner{Id: id, IP: ip, startTime: util.MakeTimestamp(), shares: make(map[int64]int64)}
	return miner
}

func (m *Miner) heartbeat() {
	now := util.MakeTimestamp()
	atomic.StoreInt64(&m.lastBeat, now)
}

func (m *Miner) getLastBeat() int64 {
	return atomic.LoadInt64(&m.lastBeat)
}

func (m *Miner) storeShare(diff int64) {
	now := util.MakeTimestamp()
	m.Lock()
	m.shares[now] += diff
	m.Unlock()
}

func (m *Miner) hashrate() int64 {
	// calculate over the smaller of 15 minutes or since miner's startTime
	now := util.MakeTimestamp()
	numPeriods := now - m.startTime
	if 900000 < numPeriods {
		numPeriods = 900000
	}

	totalShares := int64(0)
	m.Lock()
	for k, v := range m.shares {
		if k < now-900000 {
			delete(m.shares, k)
		} else {
			totalShares += v
		}
	}
	m.Unlock()
	return totalShares / numPeriods
}

func (m *Miner) processShare(s *ProxyServer, t *BlockTemplate, diff string, params []string) bool {
	paramsOrig := params[:]

	hashNoNonce := params[1]
	nonce, err := strconv.ParseUint(strings.Replace(params[0], "0x", "", -1), 16, 64)
	if err != nil {
		log.Printf("Malformed nonce: %v", err)
		return false
	}
	mixDigest := params[2]

	minerDifficulty, err := strconv.ParseFloat(diff, 64)
	if err != nil {
		log.Println("Malformed difficulty: " + diff)
		minerDifficulty = 5
	}
	minerAdjustedDifficulty := int64(minerDifficulty * 1000000 * 100)

	share := Block{
		number:      t.Height,
		hashNoNonce: common.HexToHash(hashNoNonce),
		difficulty:  big.NewInt(minerAdjustedDifficulty),
		nonce:       nonce,
		mixDigest:   common.HexToHash(mixDigest),
	}

	block := Block{
		number:      t.Height,
		hashNoNonce: common.HexToHash(hashNoNonce),
		difficulty:  t.Difficulty,
		nonce:       nonce,
		mixDigest:   common.HexToHash(mixDigest),
	}

	if hasher.Verify(share) {
		m.heartbeat()
		m.storeShare(minerAdjustedDifficulty)
		atomic.AddUint64(&m.validShares, 1)
		log.Printf("Valid share from %s@%s at difficulty %v", m.Id, m.IP, minerDifficulty)
	} else {
		atomic.AddUint64(&m.invalidShares, 1)
		log.Printf("Invalid share from %s@%s", m.Id, m.IP)
		return false
	}

	if hasher.Verify(block) {
		_, err = s.rpc().SubmitBlock(paramsOrig)
		if err != nil {
			atomic.AddUint64(&m.invalidBlocks, 1)
			atomic.AddUint64(&s.invalidBlocks, 1)
			log.Printf("Upstream share submission failure on height: %v for %v: %v", t.Height, t.Header, err)
		} else {
			s.fetchBlockTemplate()
			atomic.AddUint64(&m.validBlocks, 1)
			atomic.AddUint64(&s.validBlocks, 1)
			log.Printf("Upstream share found by miner %v@%v at height: %d", m.Id, m.IP, t.Height)
		}
	}
	return true
}

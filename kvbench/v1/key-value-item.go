// Copyright 2020 Eryx <evorui аt gmаil dοt cοm>, All rights reserved.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package kvbench

import (
	"errors"
	"fmt"
	"time"

	"github.com/hooto/hchart/v2/hcapi"
)

const (
	readKeysMax = 100000
	readKeysCap = readKeysMax * 10
)

type keyValueBenchItem struct {
	options  *keyValueBenchOptions
	status   *keyValueBenchStatus
	typ      uint64
	quit     bool
	data     chan *keyValueItem
	datasets hcapi.DataList
}

type keyValueBenchStatus struct {
	options     *keyValueBenchOptions
	ok          int64
	err         int64
	nps         float64
	npsMap      []*keyValueWriteUsageItem
	latencyMap  []*keyValueWriteUsageItem
	latencyTime int64
}

func newkeyValueBenchItem(
	options *keyValueBenchOptions) *keyValueBenchItem {
	return &keyValueBenchItem{
		options: options,
		data:    make(chan *keyValueItem, 100),
		status: &keyValueBenchStatus{
			options: options,
		},
		quit: false,
	}
}

func (it *keyValueBenchStatus) sync(v ResultStatus, tc int64) {

	//
	if v == ResultOK {
		it.ok += 1
	} else {
		it.err += 1
	}

	it.latencyTime += tc

	//
	if tc > it.options.latencyMax {
		tc = it.options.latencyMax
	} else if tc < it.options.latencyMin {
		tc = it.options.latencyMin
	}

	//
	for i := 1; i < len(it.latencyMap); i++ {
		if tc < it.latencyMap[i].time {
			it.latencyMap[i-1].num++
			break
		}
	}
}

func (it *keyValueBenchStatus) npsSet(v int64) {
	it.npsMap = append(it.npsMap, &keyValueWriteUsageItem{
		time: v,
		num:  (it.ok + it.err),
	})
}

func (it *keyValueBenchItem) dataCreate() {

	for i := uint64(1); ; i++ {

		if it.quit {
			break
		}

		if uint64Allow(it.typ, BenchTypeRandWrite) {
			it.data <- &keyValueItem{
				Key:   randKey(it.options.keySize, 0),
				Value: randValue(it.options.valueSize),
			}
		} else if uint64Allow(it.typ, BenchTypeSeqWrite) {
			it.data <- &keyValueItem{
				Key:   randKey(it.options.keySize, i),
				Value: randValue(it.options.valueSize),
			}
		}
	}
}

func (it *keyValueBenchItem) run(fn KeyValueBenchWorker) error {

	if uint64Allow(it.typ, BenchTypeRandWrite) ||
		uint64Allow(it.typ, BenchTypeSeqWrite) {
		if err := it.runWrite(fn); err != nil {
			return err
		}

	} else if it.typ == BenchTypeRandRead ||
		it.typ == BenchTypeSeqRead {
		if err := it.runRead(fn); err != nil {
			return err
		}
	}

	return nil
}

func (it *keyValueBenchItem) runWrite(fn KeyValueBenchWorker) error {

	cq := make(chan KeyValueBenchWorker, int(it.options.clientNum))
	for i := 0; i < int(it.options.clientNum); i++ {
		cq <- fn
	}

	go it.dataCreate()

	for _, v := range it.options.latencyRanges {
		it.status.latencyMap = append(it.status.latencyMap, &keyValueWriteUsageItem{
			time: v,
		})
	}

	var (
		gts      = time.Now().UnixNano() / 1e3
		ticker   = time.NewTicker(time.Duration(it.options.timeStep) * time.Second)
		timeUsed = int64(0)
	)
	defer ticker.Stop()

	it.status.npsSet(0)
	go func() {
		for {
			select {
			case _ = <-ticker.C:
				timeUsed += it.options.timeStep
				it.status.npsSet(timeUsed)
				if timeUsed >= it.options.timeLen {
					it.quit = true
				}
			}
		}
	}()

	for {

		if it.quit {
			break
		}

		v := <-it.data
		q := <-cq
		go func(q KeyValueBenchWorker, kv *keyValueItem) {

			ts := time.Now().UnixNano() / 1e3
			st := q.Write(kv.Key, kv.Value)
			tc := (time.Now().UnixNano() / 1e3) - ts

			it.status.sync(st, tc)

			cq <- q
		}(q, v)
	}

	done := 0

	for {
		select {

		case _ = <-cq:
			done += 1
			if done >= int(it.options.clientNum) {
				it.quit = true
			}
		}

		if it.quit {
			break
		}
	}

	gtc := (time.Now().UnixNano() / 1e3) - gts
	if gtc < 1 {
		gtc = 1
	}

	it.status.nps = (float64(it.status.ok+it.status.err) / float64(gtc)) * 1e6

	if it.status.ok > 0 && len(it.status.npsMap) > 0 {

		ds := hcapi.NewDataItem(it.options.dataName)
		ds.AttrSet(benchTypeName(it.typ))
		ds.AttrSet("throughput")
		ds.AttrSet(fmt.Sprintf("client-num:%d", it.options.clientNum))
		ds.AttrSet(fmt.Sprintf("key-value-size:%d-%d",
			it.options.keySize, it.options.valueSize))
		for _, av := range fn.Attrs() {
			ds.AttrSet(av)
		}

		for _, v := range it.status.npsMap {

			ds.Points = append(ds.Points, &hcapi.DataPoint{
				X: float64(v.time),
				Y: float64(v.num),
			})
		}

		it.datasets.Set(ds)
	}

	if it.status.ok > 0 && len(it.status.latencyMap) > 0 {

		ds := hcapi.NewDataItem(it.options.dataName)
		ds.AttrSet(benchTypeName(it.typ))
		ds.AttrSet("latency-avg")
		ds.AttrSet(fmt.Sprintf("client-num:%d", it.options.clientNum))
		ds.AttrSet(fmt.Sprintf("key-value-size:%d-%d",
			it.options.keySize, it.options.valueSize))
		for _, av := range fn.Attrs() {
			ds.AttrSet(av)
		}
		ds.Points = append(ds.Points, &hcapi.DataPoint{
			Y: float64Round(float64(it.status.latencyTime)/float64(it.status.ok), 4),
		})
		it.datasets.Set(ds)

		ds = hcapi.NewDataItem(it.options.dataName)
		ds.AttrSet(benchTypeName(it.typ))
		ds.AttrSet("latency")
		ds.AttrSet(fmt.Sprintf("client-num:%d", it.options.clientNum))
		ds.AttrSet(fmt.Sprintf("key-value-size:%d-%d",
			it.options.keySize, it.options.valueSize))
		for _, av := range fn.Attrs() {
			ds.AttrSet(av)
		}

		for _, v := range it.status.latencyMap {

			ds.Points = append(ds.Points, &hcapi.DataPoint{
				X: float64(v.time),
				Y: float64(v.num),
			})
		}

		it.datasets.Set(ds)
	}

	return nil
}

func (it *keyValueBenchItem) runRead(fn KeyValueBenchWorker) error {

	keys := make(chan []byte, readKeysMax+10)

	if it.typ == BenchTypeSeqRead ||
		it.typ == BenchTypeRandRead {

		for i := 0; i < readKeysCap; i++ {

			var (
				key []byte
				val = RandBytes(it.options.valueSize)
			)

			if it.typ == BenchTypeSeqRead {
				key = []byte(RandHexString(it.options.keySize-16) + uint64ToHexString(uint64(i)))
			} else {
				key = []byte(RandHexString(it.options.keySize))
			}

			fn.Write(key, val)
			if len(keys) < readKeysMax {
				keys <- key
			}
		}

	} else {
		return errors.New("invalid settings")
	}

	cq := make(chan KeyValueBenchWorker, int(it.options.clientNum))
	for i := 0; i < int(it.options.clientNum); i++ {
		cq <- fn
	}

	for _, v := range it.options.latencyRanges {
		it.status.latencyMap = append(it.status.latencyMap, &keyValueWriteUsageItem{
			time: v,
		})
	}

	var (
		gts      = time.Now().UnixNano() / 1e3
		ticker   = time.NewTicker(time.Duration(it.options.timeStep) * time.Second)
		timeUsed = int64(0)
	)
	defer ticker.Stop()

	it.status.npsSet(0)
	go func() {
		for {
			select {
			case _ = <-ticker.C:
				timeUsed += it.options.timeStep
				it.status.npsSet(timeUsed)
				if timeUsed >= it.options.timeLen {
					it.quit = true
				}
			}
		}
	}()

	for {

		if it.quit {
			break
		}

		v := <-keys
		q := <-cq
		go func(q KeyValueBenchWorker, k []byte) {

			ts := time.Now().UnixNano() / 1e3
			st := q.Read(k)
			tc := (time.Now().UnixNano() / 1e3) - ts

			it.status.sync(st, tc)

			keys <- k
			cq <- q
		}(q, v)
	}

	done := 0

	for {
		select {

		case _ = <-cq:
			done += 1
			if done >= int(it.options.clientNum) {
				it.quit = true
			}
		}

		if it.quit {
			break
		}
	}

	gtc := (time.Now().UnixNano() / 1e3) - gts
	if gtc < 1 {
		gtc = 1
	}

	it.status.nps = (float64(it.status.ok+it.status.err) / float64(gtc)) * 1e6

	if it.status.ok > 0 && len(it.status.npsMap) > 0 {

		ds := hcapi.NewDataItem(it.options.dataName)
		ds.AttrSet(benchTypeName(it.typ))
		ds.AttrSet("throughput")
		ds.AttrSet(fmt.Sprintf("client-num:%d", it.options.clientNum))
		ds.AttrSet(fmt.Sprintf("key-value-size:%d-%d",
			it.options.keySize, it.options.valueSize))
		for _, av := range fn.Attrs() {
			ds.AttrSet(av)
		}

		for _, v := range it.status.npsMap {

			ds.Points = append(ds.Points, &hcapi.DataPoint{
				X: float64(v.time),
				Y: float64(v.num),
			})
		}

		it.datasets.Set(ds)
	}

	if it.status.ok > 0 && len(it.status.latencyMap) > 0 {

		ds := hcapi.NewDataItem(it.options.dataName)
		ds.AttrSet(benchTypeName(it.typ))
		ds.AttrSet("latency-avg")
		ds.AttrSet(fmt.Sprintf("client-num:%d", it.options.clientNum))
		ds.AttrSet(fmt.Sprintf("key-value-size:%d-%d",
			it.options.keySize, it.options.valueSize))
		for _, av := range fn.Attrs() {
			ds.AttrSet(av)
		}
		ds.Points = append(ds.Points, &hcapi.DataPoint{
			Y: float64Round(float64(it.status.latencyTime)/float64(it.status.ok), 4),
		})
		it.datasets.Set(ds)

		//
		ds = hcapi.NewDataItem(it.options.dataName)
		ds.AttrSet(benchTypeName(it.typ))
		ds.AttrSet("latency")
		ds.AttrSet(fmt.Sprintf("client-num:%d", it.options.clientNum))
		ds.AttrSet(fmt.Sprintf("key-value-size:%d-%d",
			it.options.keySize, it.options.valueSize))
		for _, av := range fn.Attrs() {
			ds.AttrSet(av)
		}

		for _, v := range it.status.latencyMap {

			ds.Points = append(ds.Points, &hcapi.DataPoint{
				X: float64(v.time),
				Y: float64(v.num),
				// Y: float64Round(float64(100*v.num)/n, 4),
			})
		}

		it.datasets.Set(ds)
	}

	return nil
}

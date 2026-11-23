#!/bin/bash

for sz in 1048576 16777216 268435456 1073741824 2147483648; do
	for i in 1 2 4 8 ; do
		echo sz $sz threads $i
		./navafmt -disk=/dev/sdc -size=160000000000
		time -p GOMAXPROCS=16 ./navafsx -N=100000 -l=2147483648 -C=false -t=$i /dev/sdc
	done
done

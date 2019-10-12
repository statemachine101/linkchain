#!/bin/bash

BaseDir=/src/linkchain
DataDir=$BaseDir/data
SbinDir=$BaseDir/sbin
ConfDir=$BaseDir/conf

function Clean() {
    Stop
    rm -rf $BaseDir/data
}

function Init() {
    cd $SbinDir
    for ((nodeid=1;nodeid<=4;nodeid++)); do
        sh start.sh init $nodeid validator $DataDir
        cp -rf $ConfDir/priv_validator$nodeid.json $DataDir/validator_data/node$nodeid/config/priv_validator.json
    done
    $(nodeid=5)
    sh start.sh init $nodeid peer $DataDir
    cp -rf $ConfDir/priv_peer$nodeid.json $DataDir/peer_data/node$nodeid/config/priv_peer.json
}

function Start() {
    cd $SbinDir
    for ((nodeid=1;nodeid<=4;nodeid++)); do
        sh start.sh start $nodeid validator $DataDir
    done
    $(nodeid=5)
    sh start.sh start $nodeid peer $DataDir
}

function Stop() {
    cd $SbinDir
    for ((nodeid=1;nodeid<=4;nodeid++)); do
        sh start.sh stop $nodeid validator
    done
    $(nodeid=5)
    sh start.sh stop $nodeid peer
}

function Reset() {
    Clean
    Init
    Start
}

function Usage()
{
    echo ""
    echo "USAGE:"
    echo "command1: $0 clean"
    echo ""
    echo "command1: $0 init"
    echo ""
    echo "command1: $0 start"
    echo ""
    echo "command1: $0 stop"
    echo ""
    echo "command1: $0 reset"
    echo ""
}

case $1 in
    clean) Clean $@;;
    init) Init $@;;
    start) Start $@;;
    stop) Stop $@;;
    reset) Reset $@;;
    *) Usage;;
esac

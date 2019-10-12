#!/bin/bash
action=$1
cd $(dirname $0); DIR=$(dirname `pwd`)
VERSION=1.0.1

CLEANUP=false
CONFIRMED=false
QUIET=false
DEV="eth0"
RATE=(1000)
DELAY=(0)
LOSS=(0)
EMULATIONS=false
SRC=("any")
DST=("any")
FILTERS=()
START_IDX=2

function Usage()
{
    echo -e \
    "$0: Set up network emulation(delay, loss, bandwidth limit) for different destinations.\n" \
    "Version: $VERSION\n" \
    "Usage: $0 Emulations [Filters]\n" \
    "\n" \
    "Examples:\n" \
    "\t$0 -e eth0 -b200 -d100\n" \
    "\t# packets of eth0 from anywhere send to anywhere will be delay 100ms, limit rate 100kB.\n" \
    "\t$0 -d100,0 -l10,0 -S9561,9562,9563,any -D10.10.50.131\n" \
    "\t# packets from *:9561 to 10.10.50.131:* is delay 100ms and 10% lost\n" \
    "\t# packets from *:9562 to 10.10.50.131:* is delay 100ms and 0% lost\n" \
    "\t# packets from *:9563 to 10.10.50.131:* is delay 0ms and 10% lost\n" \
    "\t# packets from *:* to 10.10.50.131:* is delay 0ms and 0% lost\n" \
    "\t# \"any\" should be the last one, so it will be match after all the filters below\n" \
    "\n" \
    "Options:\n" \
    "\t-h, --help                          : Print this help.\n" \
    "\t-c, --clean                         : Clean rules set before.\n" \
    "\t-y, --yes                           : Answer yes for all questions.\n" \
    "\t-q, --quiet                         : Silent mode. Don't output anything.\n" \
    "\t-e, --device DEV                    : Device name (default ${DEV}).\n" \
    "[Emulation]\n" \
    "\t# num(emulations) = num(bandwidth) * num(delay) * num(loss)\n" \
    "\t-b, --bandwidth rate1 [,rate2 ...]  : Bandwidth kbit/s (default: ${RATE[0]} kb/s)\n" \
    "\t-d, --delay time1 [,time2 ...]      : Delay ms (default ${DELAY[0]} ms).\n" \
    "\t-l, --loss pct1 [,pct2 ...]         : Loss rate % (default ${LOSS[0]} %).\n" \
    "[Filter]\n" \
    "\t# num(filters) = num(source) * num(destination)\n" \
    "\t# Only one of num(source), num(destination) could be greater than 1\n" \
    "\t# Make sure: num(filters) == num(emulation)\n" \
    "\t-S, --source host1 [,host2 ...]      : Source ip:port (default ${IP[0]})\n" \
    "\t-D, --destination host1 [,host2 ...] : Destination ip:port (default ${PORT[0]})\n" \
    ""
}

function GetHost()
{
    host=$1
    if [ "$host" = "any" ]; then ip="0.0.0.0/0"; port= ; return; fi
    ip=`echo $host|grep "\."|awk -F: '{print $1}'`
    port=`echo $host|grep -v "\."`
    [ -z "$port" ] && port=`echo $host|grep ":"|awk -F: '{print $2}'`
}

function GetOpts()
{
    OPTS=`getopt -o "hcyqe:b:d:l:S:D:" -l "help,clean,yes,quiet,device:,bandwidth:,delay:,loss:,source:,destination:" -n "$0" -- "$@"`
    [ $? -ne 0 ] && exit 1
    eval set -- "$OPTS"
    IFS=","
    while true ; do
        case "$1" in
        -h|--help) Usage; exit 0;;
        -c|--clean) CLEANUP=true; shift;;
        -y|--yes) CONFIRMED=true; shift;;
        -q|--quiet) QUIET=true; shift;;
        -e|--device) DEV=($2); shift 2;;
        -b|--bandwidth) RATE=($2); EMULATIONS=true; shift 2;;
        -d|--delay) DELAY=($2); EMULATIONS=true; shift 2;;
        -l|--loss) LOSS=($2); EMULATIONS=true; shift 2;;
        -S|--source) SRC=($2); shift 2;;
        -D|--destination) DST=($2); shift 2;;
        --) shift; break;;
        esac
    done
    IFS=$OLD_IFS
    [ $# -ne 0 ] && echo "$0: invalid args $@" && exit 1
}

function CheckOpts()
{
    if ! $EMULATIONS; then
        echo "No rules of emulation, see -h"
        exit
    fi
    num_env=$((${#RATE[@]} * ${#DELAY[@]} * ${#LOSS[@]}))
    num_filter=$((${#SRC[@]} * ${#DST[@]}))
    [ $num_env -ne $num_filter ] && echo "num(filters) != num(emulations), see -h" && exit 1
    [ ${#SRC[@]} -ne 1 ] && [ ${#DST[@]} -ne 1 ] && echo "both num(SRC) and num(DST) > 1, see -h" && exit 1
    idx=$START_IDX
    if [ ${#SRC[@]} -gt 1 ]; then
        GetHost $DST
        [ -n "$ip" ] && dst=" match ip dst $ip"
        [ -n "$port" ] && dst="${dst} match ip dport $port 0xffff"
        for host in ${SRC[@]}; do
            GetHost $host
            FILTERS[idx]=$dst
            [ -n "$ip" ] && FILTERS[idx]="${FILTERS[idx]} match ip src $ip"
            [ -n "$port" ] && FILTERS[idx]="${FILTERS[idx]} match ip sport $port 0xffff"
            idx=$(($idx + 1))
        done
    else
        GetHost $SRC
        [ -n "$ip" ] && src=" match ip src $ip"
        [ -n "$port" ] && src="${src} match ip sport $port 0xffff"
        for host in ${DST[@]}; do
            GetHost $host
            FILTERS[idx]=$src
            [ -n "$ip" ] && FILTERS[idx]="${FILTERS[idx]} match ip dst $ip"
            [ -n "$port" ] && FILTERS[idx]="${FILTERS[idx]} match ip dport $port 0xffff"
            idx=$(($idx + 1))
        done
    fi
}

function CleanTcEnv()
{
    qdisc_num=`tc -d qdisc ls dev $DEV|grep -v 0:|wc -l`
    class_num=`tc -d class ls dev $DEV|grep -v 0:|wc -l`
    filter_num=`tc -d filter ls dev $DEV|grep -v 0:|wc -l`
    total=$(($qdisc_num + $class_num + $filter_num))
    if [ $total -gt 0 ]; then
        echo -en "Early rules found, clean up"
        if $CONFIRMED; then
            echo "[Y/n]?"
            echo "Y"
        else
            echo "[y/N]?"
            read input
            if [ "$input" != "y" ] && [ "$input" != "Y" ]; then
                echo "Operation cancelled."
                exit 1
            fi
        fi
        tc qdisc del dev $DEV root
    elif $CLEANUP; then
        echo "No early rules."
    fi
}

function LogDoEval()
{
    CMD_BASE="$@"
    CMD=`eval echo \"$CMD_BASE\"`
    if ! $QUIET; then echo -e "\t$CMD"; fi
    eval $CMD
}

function SetTc()
{
    QDISC_BASE="tc qdisc add dev \$DEV parent \$parent_id handle \$handle_id"
    CLASS_BASE="tc class add dev \$DEV parent \$parent_id classid \$class_id"
    U32_BASE="tc filter add dev \$DEV protocol ip parent \$parent_id prio 1 u32"
    parent_id="root"
    handle_id=1:
    idx=$START_IDX
    LogDoEval "$QDISC_BASE htb"
    for rate in ${RATE[@]}; do
        for delay in ${DELAY[@]}; do
            for loss in ${LOSS[@]}; do
                parent_id=1:
                class_id=1:$idx
                LogDoEval "$CLASS_BASE htb  rate ${rate}kbit"
                parent_id=$class_id
                handle_id=$idx:
                LogDoEval "$QDISC_BASE netem  delay ${delay}ms loss ${loss}%"
                parent_id=1:0
                LogDoEval "$U32_BASE ${FILTERS[$idx]} flowid $class_id"
                idx=$(($idx + 1))
            done
        done
    done
}

GetOpts $@
if $CLEANUP; then
    CleanTcEnv
    exit 0
fi
CheckOpts
CleanTcEnv
SetTc

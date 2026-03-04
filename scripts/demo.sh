#!/usr/bin/env bash
# CloudMarket Retry Orchestrator — Demo Script
# Sends 20+ authorization requests across diverse amounts, currencies, and processor orders.
# Usage: Start the server first with `go run main.go`, then run this script.

set -euo pipefail

BASE="http://localhost:8080"
BOLD='\033[1m'
GREEN='\033[0;32m'
YELLOW='\033[0;33m'
RED='\033[0;31m'
CYAN='\033[0;36m'
NC='\033[0m'

TOTAL=0
APPROVED=0
DECLINED=0
FAILED=0

send() {
    local label="$1"
    local amount="$2"
    local currency="$3"
    local method="$4"
    local merchant="$5"
    local processors="$6"

    TOTAL=$((TOTAL + 1))
    RESPONSE=$(curl -s -w "\n%{http_code}" -X POST "$BASE/transactions" \
        -H "Content-Type: application/json" \
        -d "{
            \"amount\": $amount,
            \"currency\": \"$currency\",
            \"payment_method\": \"$method\",
            \"merchant_id\": \"$merchant\",
            \"processor_order\": $processors
        }")
    HTTP_CODE=$(echo "$RESPONSE" | tail -1)
    BODY=$(echo "$RESPONSE" | sed '$d')
    TXN_ID=$(echo "$BODY" | python3 -c "import sys,json; print(json.load(sys.stdin).get('transaction_id','?'))" 2>/dev/null || echo "?")
    STATUS=$(echo "$BODY" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status','?'))" 2>/dev/null || echo "?")
    ATTEMPTS=$(echo "$BODY" | python3 -c "import sys,json; print(json.load(sys.stdin).get('attempts','?'))" 2>/dev/null || echo "?")

    case "$HTTP_CODE" in
        200) COLOR=$GREEN; APPROVED=$((APPROVED + 1)) ;;
        422) COLOR=$YELLOW; DECLINED=$((DECLINED + 1)) ;;
        502) COLOR=$RED; FAILED=$((FAILED + 1)) ;;
        *)   COLOR=$NC ;;
    esac

    printf "  %-3d %-45s %s${COLOR}%-10s${NC} attempts=%-2s http=%s %s\n" \
        "$TOTAL" "$label" "" "$STATUS" "$ATTEMPTS" "$HTTP_CODE" "$TXN_ID"
}

echo ""
echo -e "${BOLD}CloudMarket Retry Orchestrator — Demo (20+ requests)${NC}"
echo -e "${BOLD}=====================================================${NC}"
echo ""
echo "Note: Server uses RealisticSimulator (random outcomes). Results vary per run."
echo ""

# --- Section 1: Immediate success scenarios (various amounts/currencies) ---
echo -e "${CYAN}${BOLD}--- Success scenarios (various amounts/currencies) ---${NC}"
send "Small MXN payment"             10.00   MXN card_visa_4242     cloudmarket_mx '["StripeLatam", "PayUSouth", "EbanxBR"]'
send "Medium COP payment"           250.00   COP card_visa_4242     cloudmarket_co '["PayUSouth", "StripeLatam", "EbanxBR"]'
send "Large CLP payment"           4500.00   CLP card_mc_5555       cloudmarket_cl '["EbanxBR", "StripeLatam", "PayUSouth"]'
send "USD cross-border"             899.99   USD card_visa_4242     cloudmarket_us '["StripeLatam", "PayUSouth"]'
send "Max amount USD"              5000.00   USD card_amex_3782     cloudmarket_us '["PayUSouth", "EbanxBR", "StripeLatam"]'

# --- Section 2: Different processor orders ---
echo -e "${CYAN}${BOLD}--- Different processor orders ---${NC}"
send "EbanxBR first"                120.00   MXN card_visa_4242     cloudmarket_mx '["EbanxBR", "PayUSouth", "StripeLatam"]'
send "PayUSouth only"                75.50   COP card_mc_5555       cloudmarket_co '["PayUSouth"]'
send "StripeLatam only"              33.00   MXN card_visa_4242     cloudmarket_mx '["StripeLatam"]'
send "Reverse order"                200.00   CLP card_visa_4242     cloudmarket_cl '["EbanxBR", "PayUSouth", "StripeLatam"]'
send "Two processors"               450.00   USD card_mc_5555       cloudmarket_us '["StripeLatam", "EbanxBR"]'

# --- Section 3: Various amounts hitting different profiles ---
echo -e "${CYAN}${BOLD}--- Various amounts and card types ---${NC}"
send "Micro payment"                 10.50   MXN card_visa_4242     cloudmarket_mx '["StripeLatam", "PayUSouth", "EbanxBR"]'
send "Mid-range COP"                500.00   COP card_amex_3782     cloudmarket_co '["PayUSouth", "StripeLatam"]'
send "Premium CLP"                 3200.00   CLP card_mc_5555       cloudmarket_cl '["EbanxBR", "StripeLatam", "PayUSouth"]'
send "Near-max USD"                4999.99   USD card_visa_4242     cloudmarket_us '["StripeLatam", "PayUSouth", "EbanxBR"]'
send "Round number MXN"            1000.00   MXN card_visa_4242     cloudmarket_mx '["PayUSouth", "EbanxBR", "StripeLatam"]'

# --- Section 4: More mixed scenarios ---
echo -e "${CYAN}${BOLD}--- Mixed scenarios ---${NC}"
send "Budget COP"                    15.00   COP card_mc_5555       cloudmarket_co '["EbanxBR", "PayUSouth"]'
send "Standard MXN"                 299.99   MXN card_visa_4242     cloudmarket_mx '["StripeLatam", "PayUSouth", "EbanxBR"]'
send "High value CLP"              2750.00   CLP card_amex_3782     cloudmarket_cl '["PayUSouth", "StripeLatam", "EbanxBR"]'
send "International USD"            625.00   USD card_visa_4242     cloudmarket_us '["StripeLatam", "EbanxBR"]'
send "Final MXN"                    180.00   MXN card_visa_4242     cloudmarket_mx '["EbanxBR", "StripeLatam", "PayUSouth"]'
send "Closing COP"                  950.00   COP card_mc_5555       cloudmarket_co '["PayUSouth", "StripeLatam", "EbanxBR"]'
send "Last USD"                    1500.00   USD card_visa_4242     cloudmarket_us '["StripeLatam", "PayUSouth", "EbanxBR"]'

# --- Summary ---
echo ""
echo -e "${BOLD}--- Summary ---${NC}"
echo -e "  Total:    $TOTAL"
echo -e "  Approved: ${GREEN}$APPROVED${NC}"
echo -e "  Declined: ${YELLOW}$DECLINED${NC}"
echo -e "  Failed:   ${RED}$FAILED${NC}"

# --- Fetch first transaction details ---
echo ""
echo -e "${CYAN}${BOLD}--- Sample transaction detail (first created) ---${NC}"
FIRST_TXN=$(curl -s "$BASE/transactions" | python3 -c "import sys,json; txns=json.load(sys.stdin); print(txns[0]['transaction_id'] if txns else 'none')" 2>/dev/null || echo "none")
if [ "$FIRST_TXN" != "none" ]; then
    curl -s "$BASE/transactions/$FIRST_TXN" | python3 -m json.tool 2>/dev/null
fi

# --- List all transactions ---
echo ""
echo -e "${CYAN}${BOLD}--- All transactions (GET /transactions) ---${NC}"
curl -s "$BASE/transactions" | python3 -c "
import sys, json
txns = json.load(sys.stdin)
print(f'Total: {len(txns)} transactions')
for t in txns:
    print(f'  {t[\"transaction_id\"]}  status={t[\"status\"]:<10}  attempts={len(t[\"attempts\"])}  amount={t[\"amount\"]:>8.2f} {t[\"currency\"]}  time={t.get(\"total_processing_time_ms\",0):.1f}ms')
" 2>/dev/null

# --- Processor health ---
echo ""
echo -e "${CYAN}${BOLD}--- Processor health ---${NC}"
curl -s "$BASE/processors/health" | python3 -m json.tool 2>/dev/null

# --- Validation tests ---
echo ""
echo -e "${CYAN}${BOLD}--- Validation: invalid requests ---${NC}"
echo -n "  Missing amount:    "
curl -s -o /dev/null -w "HTTP %{http_code}" -X POST "$BASE/transactions" -H "Content-Type: application/json" -d '{"currency":"MXN","payment_method":"card","processor_order":["StripeLatam"]}'
echo ""
echo -n "  Invalid JSON:      "
curl -s -o /dev/null -w "HTTP %{http_code}" -X POST "$BASE/transactions" -H "Content-Type: application/json" -d '{bad'
echo ""
echo -n "  Unknown txn (404): "
curl -s -o /dev/null -w "HTTP %{http_code}" "$BASE/transactions/txn_doesnotexist"
echo ""

echo ""
echo -e "${GREEN}${BOLD}Demo complete!${NC}"

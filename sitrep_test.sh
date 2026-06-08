#!/bin/bash
SESSION="sitrep-test-$(date +%s)"
BASE="http://localhost:3000"

echo "🧪 Session: $SESSION"

send() {
  curl -s -X POST $BASE/v1/messages \
    -H "Content-Type: application/json" \
    -H "X-Session-ID: $SESSION" \
    -d "{\"model\":\"claude-haiku-4-5\",\"messages\":[{\"role\":\"user\",\"content\":\"$1\"}]}" > /dev/null
  echo "✓ $1"
  sleep 1
}

send "i am building a postgres connection pool in go"
send "the pool size is 20 and its timing out after 5000ms"
send "i tried increasing pool size to 50 but got connection leaks"
send "should i use pgx or database/sql for the connection pool"
send "the timeout happens specifically at 3am every night"
send "i think its a cron job hitting the database at that time"
send "how do i detect which cron job is causing the issue"
send "i checked crontab and found a backup job running at 3am"
send "the backup job opens 30 connections and never closes them"
send "i added defer rows close but still seeing leaks"
send "should i add a connection timeout to the pool config"
send "what is the recommended max idle connections setting"

echo "Done — check dashboard at http://localhost:3000/dashboard"

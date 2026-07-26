package ratelimit

import (
	"sync"
	"time"
)

type entry struct {
	count       int
	windowStart time.Time
}

type Limiter struct {
	mu     sync.Mutex
	users  map[string]*entry
	max    int
	window time.Duration
}

func New(max int, window time.Duration) *Limiter {
	return &Limiter{
		users:  make(map[string]*entry),
		max:    max,
		window: window,
	}
}

func (l *Limiter) Allow(userID string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	e, exists := l.users[userID]

	if !exists || now.Sub(e.windowStart) > l.window {
		l.users[userID] = &entry{count: 1, windowStart: now}
		return true
	}

	if e.count >= l.max {
		return false
	}

	e.count++
	return true
}

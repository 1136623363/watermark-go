package cache

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

type Redis struct {
	client    *redis.Client
	namespace string
}

func NewRedis(client *redis.Client, namespace string) (*Redis, error) {
	namespace = strings.TrimSpace(namespace)
	if client == nil {
		return nil, errors.New("redis client is required")
	}
	if namespace == "" || strings.ContainsAny(namespace, "\x00\r\n:") {
		return nil, errors.New("redis namespace is invalid")
	}
	return &Redis{client: client, namespace: namespace}, nil
}

func RedisNamespacedKey(namespace string, key Key) string {
	return "wm:" + strings.TrimSpace(namespace) + ":" + key.String()
}

func (cache *Redis) Get(ctx context.Context, key Key) ([]byte, bool, error) {
	raw, err := cache.client.Get(ctx, RedisNamespacedKey(cache.namespace, key)).Bytes()
	if err == redis.Nil {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return append([]byte(nil), raw...), true, nil
}

func (cache *Redis) Set(ctx context.Context, key Key, value []byte, ttl time.Duration) error {
	return cache.client.Set(ctx, RedisNamespacedKey(cache.namespace, key), append([]byte(nil), value...), ttl).Err()
}

func (cache *Redis) Delete(ctx context.Context, key Key) error {
	return cache.client.Del(ctx, RedisNamespacedKey(cache.namespace, key)).Err()
}

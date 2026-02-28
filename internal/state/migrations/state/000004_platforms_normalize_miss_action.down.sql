UPDATE platforms
SET reverse_proxy_miss_action = 'RANDOM'
WHERE reverse_proxy_miss_action = 'TREAT_AS_EMPTY';

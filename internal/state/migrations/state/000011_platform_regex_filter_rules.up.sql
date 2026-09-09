-- Irreversible: the unified rule model cannot be represented by older releases.
-- Restore a pre-upgrade state.db to downgrade.
UPDATE platforms
SET regex_filters_json = (
	SELECT COALESCE(json_group_array('*' || value), '[]')
	FROM json_each(platforms.regex_filters_json)
)
WHERE json_array_length(regex_filters_json) > 1;

UPDATE platforms
SET regex_filters_json = (
	SELECT json_group_array(char(92) || value)
	FROM json_each(platforms.regex_filters_json)
)
WHERE json_array_length(regex_filters_json) = 1
	AND substr(json_extract(regex_filters_json, '$[0]'), 1, 1) = '!';

UPDATE platforms
SET regex_filters_json = (
	SELECT json_group_array(value)
	FROM (
		SELECT value, 0 AS source_order, key AS rule_order
		FROM json_each(platforms.regex_filters_json)
		UNION ALL
		SELECT '!' || value, 1 AS source_order, key AS rule_order
		FROM json_each(platforms.regex_exclude_filters_json)
		ORDER BY source_order, rule_order
	)
)
WHERE json_array_length(regex_exclude_filters_json) > 0;

ALTER TABLE platforms DROP COLUMN regex_exclude_filters_json;

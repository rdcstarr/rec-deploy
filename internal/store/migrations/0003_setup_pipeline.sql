-- Which block of the manifest a deploy ran. Additive with a default, so a binary
-- that predates it keeps opening this database and reading every row.
ALTER TABLE deploys ADD COLUMN pipeline TEXT NOT NULL DEFAULT 'post_deploy';

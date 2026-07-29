ALTER TABLE articles
    DROP CONSTRAINT articles_current_step_check;

ALTER TABLE articles
    ADD CONSTRAINT articles_current_step_check
    CHECK (
        current_step IS NULL
        OR current_step IN (
            'arsenkin_collection',
            'arsenkin_cleanup',
            'structure_generation',
            'article_generation',
            'metadata_generation',
            'reading_time_calculation',
            'html_generation',
            'final_file_assembly'
        )
    );

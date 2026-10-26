-- «План продаж» of the CRM: a plan of a deal pipeline for a period (a month, a quarter, a half-year,
-- a year) — by the sum (in the base currency) or the count of the deals won in it; the targets are of the company,
-- of departments (their employees with the subdepartments' ones) and of employees.
CREATE TABLE crm_sales_plans (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    pipeline_id uuid NOT NULL REFERENCES crm_pipelines(id) ON DELETE CASCADE,
    period      text NOT NULL CHECK (period IN ('month', 'quarter', 'half', 'year')),
    starts      date NOT NULL,                     -- the first day of the period
    metric      text NOT NULL DEFAULT 'sum' CHECK (metric IN ('sum', 'count')),
    updated_by  uuid REFERENCES users(id) ON DELETE SET NULL,
    updated_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (pipeline_id, period, starts)
);

-- a target of a plan: the company's (no department, no user), a department's or an employee's
CREATE TABLE crm_sales_targets (
    plan_id       uuid NOT NULL REFERENCES crm_sales_plans(id) ON DELETE CASCADE,
    department_id uuid REFERENCES departments(id) ON DELETE CASCADE,
    user_id       uuid REFERENCES users(id) ON DELETE CASCADE,
    amount        numeric(18, 2) NOT NULL CHECK (amount > 0),
    CHECK (department_id IS NULL OR user_id IS NULL)
);
CREATE UNIQUE INDEX crm_sales_targets_uniq ON crm_sales_targets (plan_id, coalesce(department_id::text, ''), coalesce(user_id::text, ''));

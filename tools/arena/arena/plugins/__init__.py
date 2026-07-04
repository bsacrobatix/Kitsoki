"""Plugin registry. Importing a plugin module self-registers it (see base.register)."""

from . import base  # noqa: F401  (registry surface)
from . import bugfix  # noqa: F401  (self-registers the bugfix job type)
from . import paired_task  # noqa: F401  (self-registers the paired-task job type)

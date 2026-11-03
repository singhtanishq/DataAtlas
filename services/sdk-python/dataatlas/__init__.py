"""DataAtlas Python SDK.

Provides a typed client for publishing metadata and lineage events to a
DataAtlas deployment and reading catalog data through the versioned REST API.
"""

from dataatlas.client import AuthenticationError, DataAtlas, DataAtlasError

__all__ = ["DataAtlas", "DataAtlasError", "AuthenticationError"]
__version__ = "0.1.0"

from pydantic import BaseModel, Field
from typing import Optional, List


class Address(BaseModel):
    street: str
    city: str
    postal_code: str = Field(min_length=3, max_length=12)


class User(BaseModel):
    id: int
    email: str
    name: str
    addresses: List[Address] = Field(default_factory=list)
    disabled: bool = False


class UserCreate(BaseModel):
    email: str
    name: str
    password: str = Field(min_length=8)


class UserRead(BaseModel):
    id: int
    email: str
    name: str
    disabled: bool
